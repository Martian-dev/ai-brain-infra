package sync

import (
	"context"
	"time"
)

// ProviderName represents email provider types
type ProviderName string

const (
	ProviderGoogle    ProviderName = "GOOGLE"
	ProviderMicrosoft ProviderName = "MICROSOFT"
)

// MessageMeta represents normalized email metadata across providers
type MessageMeta struct {
	Provider         ProviderName
	UserID           string
	InboxID          string
	MessageID        string // provider ID (Gmail: Id, Outlook: id)
	ThreadID         string // provider thread/conversation id
	Subject          string
	Sender           string
	To               []string
	Cc               []string
	Bcc              []string
	Snippet          string
	ProviderLabels   []string
	Headers          map[string]string
	MessageDate      time.Time
}

// Checkpoint represents sync state for a provider
type Checkpoint struct {
	// Gmail: LastHistoryID; Outlook: DeltaLink (cursor)
	Cursor string
}

// MailProvider interface for provider-agnostic mail sync
type MailProvider interface {
	// InitialBackfill performs full import or deep backfill window
	InitialBackfill(ctx context.Context, user string, cp *Checkpoint, fn func(MessageMeta) error) (*Checkpoint, error)
	
	// IncrementalSync performs incremental sync from a checkpoint
	IncrementalSync(ctx context.Context, user string, cp Checkpoint, fn func(MessageMeta) error) (*Checkpoint, error)
}
