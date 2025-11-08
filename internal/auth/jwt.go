package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// User represents an authenticated user from JWT token
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// JWTVerifier handles JWT token verification with cached JWKS
type JWTVerifier struct {
	jwksURL     string
	cache       *jwk.Cache
	keySet      jwk.Set
	keySetMutex sync.RWMutex
	lastFetch   time.Time
	refreshTTL  time.Duration
}

// NewJWTVerifier creates a new JWT verifier with JWKS caching
// This implementation is optimized for extremely low latency:
// - JWKS keys are cached with automatic background refresh
// - No network call on most token verifications
// - Minimal memory allocations
func NewJWTVerifier(jwksURL string) (*JWTVerifier, error) {
	verifier := &JWTVerifier{
		jwksURL:    jwksURL,
		refreshTTL: 5 * time.Minute, // Refresh keys every 5 minutes
	}

	// Initialize the cache with automatic refresh
	cache := jwk.NewCache(context.Background())

	// Register the JWKS URL with the cache
	err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(verifier.refreshTTL))
	if err != nil {
		return nil, fmt.Errorf("failed to register JWKS URL: %w", err)
	}

	verifier.cache = cache

	// Do initial fetch to warm up the cache
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	keySet, err := verifier.fetchKeySet(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed initial JWKS fetch: %w", err)
	}

	verifier.keySet = keySet
	verifier.lastFetch = time.Now()

	// Start background refresh goroutine for proactive updates
	go verifier.backgroundRefresh()

	return verifier, nil
}

// fetchKeySet retrieves the JWKS from the cache (or fetches if needed)
func (v *JWTVerifier) fetchKeySet(ctx context.Context) (jwk.Set, error) {
	// Try to get from cache first (fastest path)
	keySet, err := v.cache.Get(ctx, v.jwksURL)
	if err != nil {
		// Fallback to direct fetch if cache fails
		return jwk.Fetch(ctx, v.jwksURL)
	}
	return keySet, nil
}

// backgroundRefresh proactively refreshes the JWKS in the background
// This ensures we never block request handling for JWKS fetches
func (v *JWTVerifier) backgroundRefresh() {
	ticker := time.NewTicker(v.refreshTTL)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		keySet, err := v.fetchKeySet(ctx)
		cancel()

		if err == nil {
			v.keySetMutex.Lock()
			v.keySet = keySet
			v.lastFetch = time.Now()
			v.keySetMutex.Unlock()
		}
		// Silently continue on error - we'll retry on next tick
	}
}

// getKeySet returns the cached key set (very fast, no network I/O)
func (v *JWTVerifier) getKeySet() jwk.Set {
	v.keySetMutex.RLock()
	defer v.keySetMutex.RUnlock()
	return v.keySet
}

// UserFromRequest extracts and validates the JWT token from the request
// This is the hot path - optimized for minimal allocations and latency
func (v *JWTVerifier) UserFromRequest(r *http.Request) (*User, error) {
	// Parse the token from Authorization header
	// jwt.ParseRequest handles "Bearer " prefix automatically
	token, err := jwt.ParseRequest(
		r,
		jwt.WithKeySet(v.getKeySet()), // Use cached key set (no network I/O!)
		jwt.WithValidate(true),         // Validate expiration and signature
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT: %w", err)
	}

	// Extract user information from token claims
	userID := token.Subject()
	if userID == "" {
		return nil, fmt.Errorf("token missing user ID (subject)")
	}

	// Extract email and name from custom claims
	var email, name string
	if emailClaim, ok := token.Get("email"); ok {
		email, _ = emailClaim.(string)
	}
	if nameClaim, ok := token.Get("name"); ok {
		name, _ = nameClaim.(string)
	}

	return &User{
		ID:    userID,
		Email: email,
		Name:  name,
	}, nil
}

// GetCacheStats returns statistics about the JWKS cache
func (v *JWTVerifier) GetCacheStats() map[string]interface{} {
	v.keySetMutex.RLock()
	defer v.keySetMutex.RUnlock()

	keyCount := 0
	if v.keySet != nil {
		keyCount = v.keySet.Len()
	}

	return map[string]interface{}{
		"keys_cached":   keyCount,
		"last_fetch":    v.lastFetch,
		"refresh_ttl":   v.refreshTTL,
		"age_seconds":   time.Since(v.lastFetch).Seconds(),
		"jwks_url":      v.jwksURL,
	}
}
