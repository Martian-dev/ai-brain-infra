package sync

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	natsjs "github.com/Martian-dev/ai-brain-infra/internal/nats"
)

// InboxConfig config for user inbox sync
type InboxConfig struct {
	UserID   string
	InboxID  string
	Provider ProviderName
	UserJWT  string // JWT to fetch tokens from BetterAuth
}

// ProviderFactory creates MailProvider
type ProviderFactory func(ctx context.Context, token *auth.Token, userID string, provider ProviderName) (MailProvider, error)

// Manager manages multi-user sync workers
type Manager struct {
	dataRoot        string
	authClient      *auth.BetterAuthClient
	publisher       *natsjs.Publisher
	providerFactory ProviderFactory
	runners         map[string]context.CancelFunc
	runnersMutex    sync.RWMutex
}

// NewManager creates sync manager
func NewManager(dataRoot string, authClient *auth.BetterAuthClient, publisher *natsjs.Publisher, providerFactory ProviderFactory) *Manager {
	return &Manager{
		dataRoot:        dataRoot,
		authClient:      authClient,
		publisher:       publisher,
		providerFactory: providerFactory,
		runners:         make(map[string]context.CancelFunc),
	}
}

// StartSync starts syncing for user inbox
func (m *Manager) StartSync(ctx context.Context, config InboxConfig) error {
	key := fmt.Sprintf("%s:%s:%s", config.UserID, config.InboxID, config.Provider)

	m.runnersMutex.Lock()
	defer m.runnersMutex.Unlock()

	if _, exists := m.runners[key]; exists {
		return fmt.Errorf("sync already running")
	}

	// Map provider
	var authProvider auth.Provider
	switch config.Provider {
	case ProviderGoogle:
		authProvider = auth.ProviderGoogle
	case ProviderMicrosoft:
		authProvider = auth.ProviderMicrosoft
	default:
		return fmt.Errorf("unsupported provider")
	}

	// Fetch token from BetterAuth
	token, err := m.authClient.GetToken(ctx, config.UserJWT, authProvider)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	// Create provider adapter
	mailProvider, err := m.providerFactory(ctx, token, config.UserID, config.Provider)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	// Create runner
	runner := &Runner{
		DataRoot:     m.dataRoot,
		AuthClient:   m.authClient,
		UserJWT:      config.UserJWT,
		Publisher:    m.publisher,
		Provider:     mailProvider,
		ProviderName: config.Provider,
	}

	// Start background worker
	runnerCtx, cancel := context.WithCancel(ctx)
	m.runners[key] = cancel

	go func() {
		log.Printf("sync start: %s", key)
		if err := runner.RunInbox(runnerCtx, config.UserID, config.InboxID); err != nil {
			log.Printf("sync error %s: %v", key, err)
		}

		m.runnersMutex.Lock()
		delete(m.runners, key)
		m.runnersMutex.Unlock()
		log.Printf("sync stop: %s", key)
	}()

	return nil
}

// StopSync stops syncing for a user inbox
func (m *Manager) StopSync(userID, inboxID string, provider ProviderName) error {
	key := fmt.Sprintf("%s:%s:%s", userID, inboxID, provider)

	m.runnersMutex.Lock()
	defer m.runnersMutex.Unlock()

	cancel, exists := m.runners[key]
	if !exists {
		return fmt.Errorf("no sync running for %s", key)
	}

	cancel()
	delete(m.runners, key)
	return nil
}

// IsRunning checks if sync is running for a user inbox
func (m *Manager) IsRunning(userID, inboxID string, provider ProviderName) bool {
	key := fmt.Sprintf("%s:%s:%s", userID, inboxID, provider)

	m.runnersMutex.RLock()
	defer m.runnersMutex.RUnlock()

	_, exists := m.runners[key]
	return exists
}

// StopAll stops all running syncs
func (m *Manager) StopAll() {
	m.runnersMutex.Lock()
	defer m.runnersMutex.Unlock()

	for key, cancel := range m.runners {
		log.Printf("Stopping sync for %s", key)
		cancel()
	}

	m.runners = make(map[string]context.CancelFunc)
}

// GetRunningSyncs returns list of currently running syncs
func (m *Manager) GetRunningSyncs() []string {
	m.runnersMutex.RLock()
	defer m.runnersMutex.RUnlock()

	var syncs []string
	for key := range m.runners {
		syncs = append(syncs, key)
	}
	return syncs
}
