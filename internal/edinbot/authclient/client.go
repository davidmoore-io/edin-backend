// Package authclient is the bot's m2m auth client. It exchanges Authentik
// client_credentials for a bearer token via a direct HTTP POST and caches it
// until shortly before expiry. The clock is injectable for deterministic
// tests.
package authclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	TokenURL     string
	ClientID     string
	ClientSecret string

	// RefreshLeadTime is how far before access_token expiry the cache treats
	// the token as stale. Default: 30 seconds.
	RefreshLeadTime time.Duration

	// Now is the clock used for expiry comparisons. Default: time.Now.
	Now func() time.Time

	// HTTPClient is used for token requests. Default: http.Client with 30s timeout.
	HTTPClient *http.Client
}

type cachedToken struct {
	accessToken string
	expiresAt   time.Time
}

type Client struct {
	cfg     Config
	mu      sync.Mutex
	cached  *cachedToken
	now     func() time.Time
	leadDur time.Duration
	http    *http.Client
}

func New(cfg Config) *Client {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	lead := cfg.RefreshLeadTime
	if lead <= 0 {
		lead = 30 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		cfg:     cfg,
		now:     now,
		leadDur: lead,
		http:    httpClient,
	}
}

// Token returns a non-expired access token, fetching or refreshing as needed.
// Implements the controlclient.TokenSource interface.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached != nil && c.now().Add(c.leadDur).Before(c.cached.expiresAt) {
		return c.cached.accessToken, nil
	}

	tok, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.cached = tok
	return tok.accessToken, nil
}

func (c *Client) fetch(ctx context.Context) (*cachedToken, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if payload.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned empty access_token")
	}

	return &cachedToken{
		accessToken: payload.AccessToken,
		expiresAt:   c.now().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}
