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

func NewUserStore(basePath string, userID int64) (*UserStore, error) {
	// Create user-specific directory structure
	userPath := filepath.Join(basePath, fmt.Sprintf("user_%d", userID))
	if err := os.MkdirAll(userPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create user directory: %w", err)
	}

	// Open SQLite database for user
	dbPath := filepath.Join(userPath, "events.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Initialize the events table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
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
	
	query += " ORDER BY created_at DESC"

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