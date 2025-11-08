package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Event struct {
	ID          int64     `json:"id"`
	Type        string    `json:"type"`
	Data        string    `json:"data"`
	CreatedAt   time.Time `json:"created_at"`
}

type UserStore struct {
	basePath string
	db       *sql.DB
}

func NewUserStore(basePath string, userID string) (*UserStore, error) {
	// Create user-specific directory structure using user ID
	userPath := filepath.Join(basePath, userID)
	if err := os.MkdirAll(userPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create user directory: %w", err)
	}

	// Open SQLite database for user with optimizations
	dbPath := filepath.Join(userPath, "events.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&cache=shared")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings for better performance
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Initialize the events table with optimized schema
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
		CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at DESC);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create events table: %w", err)
	}

	return &UserStore{
		basePath: userPath,
		db:       db,
	}, nil
}

func (s *UserStore) Close() error {
	return s.db.Close()
}

func (s *UserStore) StoreEvent(eventType, data string) (*Event, error) {
	event := &Event{
		Type:      eventType,
		Data:      data,
		CreatedAt: time.Now(),
	}

	result, err := s.db.Exec(
		"INSERT INTO events (type, data, created_at) VALUES (?, ?, ?)",
		event.Type, event.Data, event.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to store event: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get event ID: %w", err)
	}
	event.ID = id

	return event, nil
}

func (s *UserStore) GetEvents(eventType string) ([]Event, error) {
	query := "SELECT id, type, data, created_at FROM events"
	args := []interface{}{}
	
	if eventType != "" {
		query += " WHERE type = ?"
		args = append(args, eventType)
	}
	
	query += " ORDER BY created_at DESC LIMIT 1000" // Limit for performance

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Type, &event.Data, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}

	return events, nil
}