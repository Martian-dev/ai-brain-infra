package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store represents a per-user event store
type Store struct {
	DB *sql.DB
}

// OutboxMessage represents a message in the outbox
type OutboxMessage struct {
	ID      int64
	Subject string
	Payload []byte
	MsgID   string
}

// OpenUserDB opens or creates a per-user event database
func OpenUserDB(dbPath string) (*Store, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open database with optimized settings
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Apply schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	return &Store{DB: db}, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.DB.Close()
}

// AppendEmailReceivedTx appends an email event and outbox entry in a transaction
func (s *Store) AppendEmailReceivedTx(
	ctx context.Context,
	tx *sql.Tx,
	eventID string,
	ts int64,
	msgDate int64,
	provider string,
	inboxID string,
	userID string,
	providerMessageID string,
	providerThreadID string,
	subject string,
	sender string,
	toAddrs string,
	ccAddrs string,
	bccAddrs string,
	snippet string,
	headersJSON string,
	labelsJSON string,
	natsSubject string,
	eventType string,
	payload []byte,
	msgID string,
) error {
	// Insert email event (UNIQUE constraint on provider+message_id prevents duplicates)
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO email_received_events
		(event_id, ts, msg_date, provider, inbox_id, user_id, provider_message_id, provider_thread_id,
		 subject, sender, to_addrs, cc_addrs, bcc_addrs, snippet, headers_json, labels_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, eventID, ts, msgDate, provider, inboxID, userID, providerMessageID, providerThreadID,
		subject, sender, toAddrs, ccAddrs, bccAddrs, snippet, headersJSON, labelsJSON)
	
	if err != nil {
		return fmt.Errorf("failed to insert email event: %w", err)
	}

	// Insert outbox entry
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox (ts, subject, event_type, payload, msg_id, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, time.Now().Unix(), natsSubject, eventType, payload, msgID, time.Now().Unix())
	
	if err != nil {
		return fmt.Errorf("failed to insert outbox entry: %w", err)
	}

	return nil
}

// DequeueOutbox fetches unpublished messages from outbox
func (s *Store) DequeueOutbox(ctx context.Context, limit int) ([]OutboxMessage, error) {
	now := time.Now().Unix()
	
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, subject, payload, msg_id
		FROM outbox
		WHERE published_at IS NULL
		  AND next_attempt_at <= ?
		ORDER BY id
		LIMIT ?
	`, now, limit)
	
	if err != nil {
		return nil, fmt.Errorf("failed to query outbox: %w", err)
	}
	defer rows.Close()

	var messages []OutboxMessage
	for rows.Next() {
		var msg OutboxMessage
		if err := rows.Scan(&msg.ID, &msg.Subject, &msg.Payload, &msg.MsgID); err != nil {
			return nil, fmt.Errorf("failed to scan outbox row: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

// MarkPublished marks an outbox message as published
func (s *Store) MarkPublished(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE outbox SET published_at = ? WHERE id = ?
	`, time.Now().Unix(), id)
	
	if err != nil {
		return fmt.Errorf("failed to mark published: %w", err)
	}
	
	return nil
}

// MarkOutboxRetry updates retry count and next attempt time
func (s *Store) MarkOutboxRetry(ctx context.Context, id int64, backoff time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE outbox 
		SET retries = retries + 1,
		    next_attempt_at = ?
		WHERE id = ?
	`, time.Now().Add(backoff).Unix(), id)
	
	if err != nil {
		return fmt.Errorf("failed to mark retry: %w", err)
	}
	
	return nil
}

// LoadCheckpoint loads sync checkpoint for a provider
func (s *Store) LoadCheckpoint(ctx context.Context, provider string) (string, error) {
	var cursor sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT cursor FROM provider_sync_state WHERE provider = ?
	`, provider).Scan(&cursor)
	
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("failed to load checkpoint: %w", err)
	}
	
	return cursor.String, nil
}

// SaveCheckpoint saves sync checkpoint for a provider
func (s *Store) SaveCheckpoint(ctx context.Context, provider, inboxID, cursor, status string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO provider_sync_state (provider, inbox_id, cursor, last_synced_at, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			cursor = excluded.cursor,
			last_synced_at = excluded.last_synced_at,
			status = excluded.status,
			updated_at = excluded.updated_at
	`, provider, inboxID, cursor, time.Now().Unix(), status, time.Now().Unix())
	
	if err != nil {
		return fmt.Errorf("failed to save checkpoint: %w", err)
	}
	
	return nil
}

// UpdateSyncStatus updates sync status with error info
func (s *Store) UpdateSyncStatus(ctx context.Context, provider, status, errorMsg string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE provider_sync_state
		SET status = ?,
		    last_error = ?,
		    retry_count = CASE WHEN ? != '' THEN retry_count + 1 ELSE retry_count END,
		    updated_at = ?
		WHERE provider = ?
	`, status, errorMsg, errorMsg, time.Now().Unix(), provider)
	
	return err
}
