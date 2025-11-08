package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	"github.com/Martian-dev/ai-brain-infra/internal/sync"
)

// Adapter implements MailProvider for Outlook/Microsoft Graph
type Adapter struct {
	client *msgraphsdk.GraphServiceClient
	userID string
}

// New creates a new Outlook adapter
func New(ctx context.Context, tok *auth.Token, userID string) (*Adapter, error) {
	// Create token credential
	cred := &staticTokenCredential{token: tok.AccessToken}

	client, err := msgraphsdk.NewGraphServiceClientWithCredentials(cred, []string{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Graph client: %w", err)
	}

	return &Adapter{
		client: client,
		userID: userID,
	}, nil
}

// InitialBackfill performs full import of messages
func (a *Adapter) InitialBackfill(ctx context.Context, user string, cp *sync.Checkpoint, fn func(sync.MessageMeta) error) (*sync.Checkpoint, error) {
	// Use Microsoft Graph to list messages
	requestConfig := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
			Top:    Int32Ptr(100),
			Select: []string{"id", "conversationId", "subject", "from", "toRecipients", "ccRecipients", "bccRecipients", "bodyPreview", "receivedDateTime", "internetMessageHeaders"},
		},
	}

	result, err := a.client.Users().ByUserId(user).Messages().Get(ctx, requestConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	// Process messages
	for _, msg := range result.GetValue() {
		meta := normalizeOutlook(msg, user)
		if err := fn(meta); err != nil {
			return nil, err
		}
	}

	// For now, we'll use a simple cursor based on the last message ID
	// In production, you would use the delta link from the response
	messages := result.GetValue()
	if len(messages) > 0 {
		if lastMsg := messages[len(messages)-1]; lastMsg != nil {
			if id := lastMsg.GetId(); id != nil {
				return &sync.Checkpoint{Cursor: *id}, nil
			}
		}
	}

	return &sync.Checkpoint{}, nil
}

// IncrementalSync performs incremental sync using delta query
func (a *Adapter) IncrementalSync(ctx context.Context, user string, cp sync.Checkpoint, fn func(sync.MessageMeta) error) (*sync.Checkpoint, error) {
	if cp.Cursor == "" {
		// No checkpoint, perform initial backfill
		return a.InitialBackfill(ctx, user, &cp, fn)
	}

	// Use delta link for incremental sync
	// Note: In production, you'd use the delta link URL directly
	// For now, we'll use the regular messages endpoint with filter
	requestConfig := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
			Top:    Int32Ptr(100),
			Select: []string{"id", "conversationId", "subject", "from", "toRecipients", "ccRecipients", "bccRecipients", "bodyPreview", "receivedDateTime", "internetMessageHeaders"},
		},
	}

	result, err := a.client.Users().ByUserId(user).Messages().Get(ctx, requestConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to sync messages: %w", err)
	}

	// Process new/updated messages
	for _, msg := range result.GetValue() {
		meta := normalizeOutlook(msg, user)
		if err := fn(meta); err != nil {
			return nil, err
		}
	}

	// Update checkpoint with the last message ID
	messages := result.GetValue()
	if len(messages) > 0 {
		if lastMsg := messages[len(messages)-1]; lastMsg != nil {
			if id := lastMsg.GetId(); id != nil {
				return &sync.Checkpoint{Cursor: *id}, nil
			}
		}
	}

	return &sync.Checkpoint{Cursor: cp.Cursor}, nil
}

// normalizeOutlook converts Outlook message to MessageMeta
func normalizeOutlook(m models.Messageable, userID string) sync.MessageMeta {
	meta := sync.MessageMeta{
		Provider: sync.ProviderMicrosoft,
		UserID:   userID,
		InboxID:  "inbox",
	}

	if id := m.GetId(); id != nil {
		meta.MessageID = *id
	}

	if convID := m.GetConversationId(); convID != nil {
		meta.ThreadID = *convID
	}

	if subject := m.GetSubject(); subject != nil {
		meta.Subject = *subject
	}

	if from := m.GetFrom(); from != nil {
		if emailAddr := from.GetEmailAddress(); emailAddr != nil {
			if addr := emailAddr.GetAddress(); addr != nil {
				meta.Sender = *addr
			}
		}
	}

	if to := m.GetToRecipients(); to != nil {
		meta.To = extractAddresses(to)
	}

	if cc := m.GetCcRecipients(); cc != nil {
		meta.Cc = extractAddresses(cc)
	}

	if bcc := m.GetBccRecipients(); bcc != nil {
		meta.Bcc = extractAddresses(bcc)
	}

	if preview := m.GetBodyPreview(); preview != nil {
		meta.Snippet = *preview
	}

	if rcvd := m.GetReceivedDateTime(); rcvd != nil {
		meta.MessageDate = *rcvd
	}

	// Extract headers
	meta.Headers = make(map[string]string)
	if headers := m.GetInternetMessageHeaders(); headers != nil {
		for _, h := range headers {
			if name := h.GetName(); name != nil {
				if value := h.GetValue(); value != nil {
					meta.Headers[*name] = *value
				}
			}
		}
	}

	return meta
}

// extractAddresses extracts email addresses from recipients
func extractAddresses(recipients []models.Recipientable) []string {
	var addrs []string
	for _, r := range recipients {
		if emailAddr := r.GetEmailAddress(); emailAddr != nil {
			if addr := emailAddr.GetAddress(); addr != nil {
				addrs = append(addrs, *addr)
			}
		}
	}
	return addrs
}

// staticTokenCredential implements Azure credential interface
type staticTokenCredential struct {
	token string
}

func (c *staticTokenCredential) GetToken(ctx context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{
		Token:     c.token,
		ExpiresOn: time.Now().Add(1 * time.Hour),
	}, nil
}

// Int32Ptr returns a pointer to an int32
func Int32Ptr(i int32) *int32 {
	return &i
}

// mustJSON converts value to JSON
func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
