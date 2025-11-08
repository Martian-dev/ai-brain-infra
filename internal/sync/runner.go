package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	"github.com/Martian-dev/ai-brain-infra/internal/eventstore/sqlite"
	natsjs "github.com/Martian-dev/ai-brain-infra/internal/nats"
)

// Runner orchestrates mail sync for user inbox
type Runner struct {
	DataRoot     string
	AuthClient   *auth.BetterAuthClient
	UserJWT      string
	Publisher    *natsjs.Publisher
	Provider     MailProvider
	ProviderName ProviderName
}

// RunInbox runs continuous sync for a user inbox
func (r *Runner) RunInbox(ctx context.Context, userID, inboxID string) error {
	dbPath := filepath.Join(r.DataRoot, userID, "events.db")
	store, err := sqlite.OpenUserDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open user DB: %w", err)
	}
	defer store.Close()

	// Ensure NATS stream exists
	if err := r.Publisher.EnsureStream(ctx); err != nil {
		return fmt.Errorf("failed to ensure NATS stream: %w", err)
	}

	// Start outbox dispatcher in background
	go r.dispatchLoop(ctx, store)

	// Load checkpoint
	cursor, err := store.LoadCheckpoint(ctx, string(r.ProviderName))
	if err != nil {
		log.Printf("Error loading checkpoint: %v", err)
	}

	cp := Checkpoint{Cursor: cursor}

	// Processor function for messages
	proc := r.createProcessor(ctx, store, userID, inboxID)

	// Perform initial or incremental sync
	var newCP *Checkpoint
	if cp.Cursor == "" {
		log.Printf("Starting initial backfill for user %s", userID)
		if err := store.SaveCheckpoint(ctx, string(r.ProviderName), inboxID, "", "SYNCING"); err != nil {
			log.Printf("Error saving checkpoint: %v", err)
		}
		newCP, err = r.Provider.InitialBackfill(ctx, "me", &cp, proc)
	} else {
		log.Printf("Starting incremental sync for user %s from cursor %s", userID, cp.Cursor)
		if err := store.SaveCheckpoint(ctx, string(r.ProviderName), inboxID, cp.Cursor, "SYNCING"); err != nil {
			log.Printf("Error saving checkpoint: %v", err)
		}
		newCP, err = r.Provider.IncrementalSync(ctx, "me", cp, proc)
	}

	if err != nil {
		_ = store.UpdateSyncStatus(ctx, string(r.ProviderName), "ERROR", err.Error())
		return fmt.Errorf("sync failed: %w", err)
	}

	// Save new checkpoint
	if newCP != nil {
		if err := store.SaveCheckpoint(ctx, string(r.ProviderName), inboxID, newCP.Cursor, "HOOKED"); err != nil {
			log.Printf("Error saving checkpoint: %v", err)
		}
	}

	log.Printf("Initial sync complete for user %s", userID)

	// Start continuous incremental sync loop
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Stopping sync for user %s", userID)
			return nil
		case <-ticker.C:
			// Load current checkpoint
			cursor, err := store.LoadCheckpoint(ctx, string(r.ProviderName))
			if err != nil {
				log.Printf("Error loading checkpoint: %v", err)
				continue
			}

			cp := Checkpoint{Cursor: cursor}
			if cp.Cursor == "" {
				continue
			}

			// Incremental sync
			newCP, err := r.Provider.IncrementalSync(ctx, "me", cp, proc)
			if err != nil {
				log.Printf("Incremental sync error for user %s: %v", userID, err)
				_ = store.UpdateSyncStatus(ctx, string(r.ProviderName), "ERROR", err.Error())
				continue
			}

			// Save new checkpoint
			if newCP != nil && newCP.Cursor != cp.Cursor {
				if err := store.SaveCheckpoint(ctx, string(r.ProviderName), inboxID, newCP.Cursor, "HOOKED"); err != nil {
					log.Printf("Error saving checkpoint: %v", err)
				}
				log.Printf("Synced new messages for user %s, new cursor: %s", userID, newCP.Cursor)
			}
		}
	}
}

// createProcessor creates a message processor function
func (r *Runner) createProcessor(ctx context.Context, store *sqlite.Store, userID, inboxID string) func(MessageMeta) error {
	return func(meta MessageMeta) error {
		// Create event
		eventID := uuid.NewString()
		ts := time.Now().Unix()
		msgDate := meta.MessageDate.Unix()

		// Serialize arrays and maps to JSON
		toAddrsJSON, _ := json.Marshal(meta.To)
		ccAddrsJSON, _ := json.Marshal(meta.Cc)
		bccAddrsJSON, _ := json.Marshal(meta.Bcc)
		headersJSON, _ := json.Marshal(meta.Headers)
		labelsJSON, _ := json.Marshal(meta.ProviderLabels)

		// Create event payload for NATS
		event := map[string]interface{}{
			"event_id":            eventID,
			"ts":                  ts,
			"msg_date":            msgDate,
			"provider":            string(meta.Provider),
			"inbox_id":            inboxID,
			"user_id":             userID,
			"provider_message_id": meta.MessageID,
			"provider_thread_id":  meta.ThreadID,
			"subject":             meta.Subject,
			"sender":              meta.Sender,
			"to_addrs":            meta.To,
			"cc_addrs":            meta.Cc,
			"bcc_addrs":           meta.Bcc,
			"snippet":             meta.Snippet,
			"headers":             meta.Headers,
			"labels":              meta.ProviderLabels,
		}

		payload, _ := json.Marshal(event)
		msgID := fmt.Sprintf("email.received|%s|%s", meta.Provider, meta.MessageID)
		subject := fmt.Sprintf("user.%s.email.received", userID)

		// Start transaction
		tx, err := store.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}

		// Append email event and outbox entry
		err = store.AppendEmailReceivedTx(
			ctx, tx,
			eventID,
			ts,
			msgDate,
			string(meta.Provider),
			inboxID,
			userID,
			meta.MessageID,
			meta.ThreadID,
			meta.Subject,
			meta.Sender,
			string(toAddrsJSON),
			string(ccAddrsJSON),
			string(bccAddrsJSON),
			meta.Snippet,
			string(headersJSON),
			string(labelsJSON),
			subject,
			"email.received",
			payload,
			msgID,
		)

		if err != nil {
			_ = tx.Rollback()
			// Ignore duplicate errors (UNIQUE constraint violations)
			return nil
		}

		// Commit transaction
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}

		return nil
	}
}

// dispatchLoop continuously dispatches messages from outbox to NATS
func (r *Runner) dispatchLoop(ctx context.Context, store *sqlite.Store) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Dequeue outbox messages
		messages, err := store.DequeueOutbox(ctx, 100)
		if err != nil {
			log.Printf("Error dequeuing outbox: %v", err)
			time.Sleep(time.Second)
			continue
		}

		if len(messages) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Publish each message
		for _, msg := range messages {
			err := r.Publisher.Publish(msg.Subject, msg.Payload, msg.MsgID)
			if err != nil {
				log.Printf("Error publishing message %d: %v", msg.ID, err)
				// Mark for retry with backoff
				_ = store.MarkOutboxRetry(ctx, msg.ID, 10*time.Second)
				continue
			}

			// Mark as published
			if err := store.MarkPublished(ctx, msg.ID); err != nil {
				log.Printf("Error marking message %d as published: %v", msg.ID, err)
			}
		}
	}
}
