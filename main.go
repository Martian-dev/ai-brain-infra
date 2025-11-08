package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	natsjs "github.com/Martian-dev/ai-brain-infra/internal/nats"
	"github.com/Martian-dev/ai-brain-infra/internal/providers/gmail"
	"github.com/Martian-dev/ai-brain-infra/internal/providers/outlook"
	"github.com/Martian-dev/ai-brain-infra/internal/store"
	"github.com/Martian-dev/ai-brain-infra/internal/sync"
	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

var (
	jwtVerifier *auth.JWTVerifier
	syncManager *sync.Manager
)

type EventRequest struct {
	Type string `json:"type" binding:"required"`
	Data string `json:"data" binding:"required"`
}

func main() {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll("data/users", 0755); err != nil {
		log.Fatal(err)
	}

	// Get JWKS URL from environment or use default
	jwksURL := os.Getenv("BETTER_AUTH_JWKS_URL")
	if jwksURL == "" {
		jwksURL = "http://localhost:3000/api/auth/jwks"
	}

	// Initialize JWT verifier with JWKS caching
	var err error
	jwtVerifier, err = auth.NewJWTVerifier(jwksURL)
	if err != nil {
		log.Fatalf("Failed to initialize JWT verifier: %v", err)
	}
	log.Printf("✓ JWT verifier initialized with JWKS from: %s", jwksURL)

	// Initialize NATS publisher
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	
	publisher, err := natsjs.NewPublisher(natsURL)
	if err != nil {
		log.Fatalf("Failed to initialize NATS publisher: %v", err)
	}
	defer publisher.Close()
	log.Printf("✓ NATS publisher: %s", natsURL)

	// Initialize BetterAuth client for OAuth tokens
	authServerURL := os.Getenv("BETTER_AUTH_URL")
	if authServerURL == "" {
		authServerURL = "http://localhost:3000"
	}
	
	authClient := auth.NewBetterAuthClient(authServerURL)
	log.Printf("✓ BetterAuth client: %s", authServerURL)

	// Provider factory
	providerFactory := func(ctx context.Context, token *auth.Token, userID string, provider sync.ProviderName) (sync.MailProvider, error) {
		switch provider {
		case sync.ProviderGoogle:
			return gmail.New(ctx, token)
		case sync.ProviderMicrosoft:
			return outlook.New(ctx, token, userID)
		default:
			return nil, nil
		}
	}

	// Initialize sync manager
	syncManager = sync.NewManager(
		filepath.Join("data", "users"),
		authClient,
		publisher,
		providerFactory,
	)
	log.Printf("✓ Sync manager ready")

	// Set Gin to release mode for production (can be overridden with GIN_MODE env var)
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

	// Health check endpoint - no auth required
	r.GET("/health", func(c *gin.Context) {
		stats := jwtVerifier.GetCacheStats()
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"service": "ai-brain-api",
			"jwks_cache": stats,
		})
	})

	// Protected routes - all require JWT authentication
	authorized := r.Group("/")
	authorized.Use(jwtAuthMiddleware())

	// Store event endpoint
	authorized.POST("/events", func(c *gin.Context) {
		var req EventRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Get user from context (set by middleware)
		user, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found in context"})
			return
		}

		authUser := user.(*auth.User)
		
		// Use user ID for storage (not username)
		userStore, err := store.NewUserStore(filepath.Join("data", "users"), authUser.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer userStore.Close()

		event, err := userStore.StoreEvent(req.Type, req.Data)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, event)
	})

	// Get events endpoint
	authorized.GET("/events", func(c *gin.Context) {
		eventType := c.Query("type") // Optional filter by event type

		// Get user from context
		user, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found in context"})
			return
		}

		authUser := user.(*auth.User)

		// Use user ID for storage
		userStore, err := store.NewUserStore(filepath.Join("data", "users"), authUser.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer userStore.Close()

		events, err := userStore.GetEvents(eventType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, events)
	})

	// Get current user info endpoint
	authorized.GET("/me", func(c *gin.Context) {
		user, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found in context"})
			return
		}

		c.JSON(http.StatusOK, user)
	})

	// Mail sync endpoints
	
	// Connect mail - BetterAuth already has OAuth tokens
	authorized.POST("/mail/connect", func(c *gin.Context) {
		var req struct {
			Provider string `json:"provider" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, _ := c.Get("user")
		authUser := user.(*auth.User)

		// Map provider
		var syncProvider sync.ProviderName
		switch req.Provider {
		case "google", "GOOGLE":
			syncProvider = sync.ProviderGoogle
		case "microsoft", "MICROSOFT":
			syncProvider = sync.ProviderMicrosoft
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
			return
		}

		// Get JWT from header
		jwt := c.GetHeader("Authorization")
		if jwt == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		jwt = jwt[7:] // Remove "Bearer "

		// Start sync - tokens fetched from BetterAuth automatically
		config := sync.InboxConfig{
			UserID:   authUser.ID,
			InboxID:  "primary",
			Provider: syncProvider,
			UserJWT:  jwt,
		}

		if err := syncManager.StartSync(context.Background(), config); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "sync started",
			"provider": req.Provider,
		})
	})

	// Get sync status
	authorized.GET("/mail/status", func(c *gin.Context) {
		user, _ := c.Get("user")
		authUser := user.(*auth.User)

		running := syncManager.GetRunningSyncs()
		var userSyncs []string
		for _, key := range running {
			if len(key) > len(authUser.ID) && key[:len(authUser.ID)] == authUser.ID {
				userSyncs = append(userSyncs, key)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"user_id":       authUser.ID,
			"running_syncs": userSyncs,
		})
	})

	// Stop mail sync
	authorized.POST("/mail/disconnect", func(c *gin.Context) {
		var req struct {
			Provider string `json:"provider" binding:"required"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, _ := c.Get("user")
		authUser := user.(*auth.User)

		var provider sync.ProviderName
		switch req.Provider {
		case "google", "GOOGLE":
			provider = sync.ProviderGoogle
		case "microsoft", "MICROSOFT":
			provider = sync.ProviderMicrosoft
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
			return
		}

		if err := syncManager.StopSync(authUser.ID, "primary", provider); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "mail sync stopped"})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("🚀 AI Brain API server starting on port %s", port)
	log.Fatal(r.Run(":" + port))
}

// jwtAuthMiddleware validates JWT tokens using the JWX library with JWKS caching
// This middleware is optimized for extremely low latency:
// - Uses cached JWKS (no network I/O on most requests)
// - Minimal allocations
// - Fast-path validation
func jwtAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract and validate JWT token
		user, err := jwtVerifier.UserFromRequest(c.Request)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		// Store user in context for handlers to use
		c.Set("user", user)
		c.Next()
	}
}
