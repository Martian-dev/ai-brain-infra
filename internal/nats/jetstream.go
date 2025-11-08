package natsjs

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Publisher wraps NATS JetStream for publishing events
type Publisher struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

// NewPublisher creates a new NATS JetStream publisher
func NewPublisher(url string) (*Publisher, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to get JetStream context: %w", err)
	}

	return &Publisher{nc: nc, js: js}, nil
}

// EnsureStream ensures the USER_EVENTS stream exists
func (p *Publisher) EnsureStream(ctx context.Context) error {
	// Check if stream exists
	streamInfo, err := p.js.StreamInfo("USER_EVENTS")
	if err == nil && streamInfo != nil {
		return nil // Stream already exists
	}

	// Create stream
	_, err = p.js.AddStream(&nats.StreamConfig{
		Name:       "USER_EVENTS",
		Subjects:   []string{"user.*.>"},
		Storage:    nats.FileStorage,
		Retention:  nats.LimitsPolicy,
		Duplicates: 10 * time.Minute,
		MaxAge:     30 * 24 * time.Hour, // Keep events for 30 days
	})

	if err != nil {
		// Check if error is "stream name already in use"
		if err.Error() == "stream name already in use" || err == nats.ErrStreamNameAlreadyInUse {
			return nil
		}
		return fmt.Errorf("failed to create stream: %w", err)
	}

	return nil
}

// Publish publishes a message to NATS JetStream with deduplication
func (p *Publisher) Publish(subject string, payload []byte, msgID string) error {
	_, err := p.js.Publish(subject, payload, nats.MsgId(msgID))
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}
	return nil
}

// Close closes the NATS connection
func (p *Publisher) Close() {
	if p.nc != nil {
		p.nc.Close()
	}
}
