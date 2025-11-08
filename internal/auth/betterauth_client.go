package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Provider represents OAuth providers
type Provider string

const (
	ProviderGoogle    Provider = "google"
	ProviderMicrosoft Provider = "microsoft"
)

// Token represents OAuth tokens
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// BetterAuthClient fetches OAuth tokens from BetterAuth
type BetterAuthClient struct {
	baseURL string
	client  *http.Client
}

// NewBetterAuthClient creates client to fetch tokens from BetterAuth
func NewBetterAuthClient(authServerURL string) *BetterAuthClient {
	return &BetterAuthClient{
		baseURL: authServerURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// GetToken fetches OAuth token from BetterAuth using user's JWT
// BetterAuth handles storage, refresh, everything
func (c *BetterAuthClient) GetToken(ctx context.Context, userJWT string, provider Provider) (*Token, error) {
	url := fmt.Sprintf("%s/api/auth/accounts/%s/token", c.baseURL, provider)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+userJWT)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("no %s account connected", provider)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bad status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"` // unix timestamp
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		Expiry:       time.Unix(result.ExpiresAt, 0),
	}, nil
}
