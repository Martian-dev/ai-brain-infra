package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	"github.com/Martian-dev/ai-brain-infra/internal/sync"
)

// Adapter implements MailProvider for Gmail
type Adapter struct {
	svc *gmail.Service
}

// New creates a new Gmail adapter
func New(ctx context.Context, tok *auth.Token) (*Adapter, error) {
	// Create OAuth2 client
	oauth2Token := &oauth2.Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
	}

	config := &oauth2.Config{
		Scopes: []string{gmail.GmailReadonlyScope},
	}

	httpClient := config.Client(ctx, oauth2Token)

	svc, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gmail service: %w", err)
	}

	return &Adapter{svc: svc}, nil
}

// InitialBackfill performs full import of messages
func (a *Adapter) InitialBackfill(ctx context.Context, user string, cp *sync.Checkpoint, fn func(sync.MessageMeta) error) (*sync.Checkpoint, error) {
	// List all messages (paginated)
	call := a.svc.Users.Messages.List(user).IncludeSpamTrash(false).MaxResults(100)

	err := call.Pages(ctx, func(page *gmail.ListMessagesResponse) error {
		for _, m := range page.Messages {
			// Fetch message metadata only (requires gmail.metadata scope)
			meta, err := a.svc.Users.Messages.Get(user, m.Id).Format("metadata").Do()
			if err != nil {
				return fmt.Errorf("failed to get message %s: %w", m.Id, err)
			}

			normalized := normalize(meta, user)
			if err := fn(normalized); err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to backfill messages: %w", err)
	}

	// Get current history ID as checkpoint
	profile, err := a.svc.Users.GetProfile(user).Do()
	if err == nil && profile.HistoryId != 0 {
		return &sync.Checkpoint{Cursor: fmt.Sprintf("%d", profile.HistoryId)}, nil
	}

	return &sync.Checkpoint{}, nil
}

// IncrementalSync performs incremental sync from checkpoint
func (a *Adapter) IncrementalSync(ctx context.Context, user string, cp sync.Checkpoint, fn func(sync.MessageMeta) error) (*sync.Checkpoint, error) {
	if cp.Cursor == "" {
		// No checkpoint, perform initial backfill
		return a.InitialBackfill(ctx, user, &cp, fn)
	}

	// Parse history ID from cursor
	startHistoryID, err := strconv.ParseUint(cp.Cursor, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid history ID in cursor: %w", err)
	}

	// Call History API
	call := a.svc.Users.History.List(user).StartHistoryId(startHistoryID).MaxResults(100)

	var latestHistoryID uint64 = startHistoryID
	processedMessages := make(map[string]bool)

	err = call.Pages(ctx, func(page *gmail.ListHistoryResponse) error {
		for _, history := range page.History {
			// Update latest history ID
			if history.Id > latestHistoryID {
				latestHistoryID = history.Id
			}

			// Process new messages
			for _, record := range history.MessagesAdded {
				msgID := record.Message.Id
				if processedMessages[msgID] {
					continue
				}
				processedMessages[msgID] = true

				// Fetch metadata only
				meta, err := a.svc.Users.Messages.Get(user, msgID).Format("metadata").Do()
				if err != nil {
					return fmt.Errorf("failed to get message %s: %w", msgID, err)
				}

				normalized := normalize(meta, user)
				if err := fn(normalized); err != nil {
					return err
				}
			}
		}
		return nil
	})

	if err != nil {
		// Check if history ID is too old
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "historyId") {
			// Fall back to full rescan
			return a.InitialBackfill(ctx, user, &cp, fn)
		}
		return nil, fmt.Errorf("failed to sync history: %w", err)
	}

	return &sync.Checkpoint{Cursor: fmt.Sprintf("%d", latestHistoryID)}, nil
}

// normalize converts Gmail message to MessageMeta
func normalize(m *gmail.Message, userID string) sync.MessageMeta {
	headers := make(map[string]string)
	for _, kv := range m.Payload.Headers {
		headers[kv.Name] = kv.Value
	}

	return sync.MessageMeta{
		Provider:       sync.ProviderGoogle,
		UserID:         userID,
		InboxID:        "primary", // Could be parsed from labels
		MessageID:      m.Id,
		ThreadID:       m.ThreadId,
		Subject:        headers["Subject"],
		Sender:         headers["From"],
		To:             splitAddrs(headers["To"]),
		Cc:             splitAddrs(headers["Cc"]),
		Bcc:            splitAddrs(headers["Bcc"]),
		Snippet:        m.Snippet,
		ProviderLabels: m.LabelIds,
		Headers:        headers,
		MessageDate:    time.UnixMilli(m.InternalDate),
	}
}

// splitAddrs parses comma-separated email addresses
func splitAddrs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// mustJSON converts value to JSON
func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
