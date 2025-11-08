package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Martian-dev/ai-brain-infra/internal/auth"
	"github.com/Martian-dev/ai-brain-infra/internal/store"
	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

var (
	jwtVerifier *auth.JWTVerifier
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
