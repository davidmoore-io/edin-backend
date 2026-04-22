package frontier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenResponse is Frontier's token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`    // seconds
	RefreshToken string `json:"refresh_token"` // may be absent
}

// MeResponse is the /me endpoint response.
type MeResponse struct {
	CustomerID string `json:"customer_id"`
}

// ProfileResponse is the CAPI /profile endpoint response.
type ProfileResponse struct {
	Commander struct {
		Name string `json:"name"`
	} `json:"commander"`
}

// Client makes authenticated calls to Frontier's auth and CAPI endpoints.
type Client struct {
	httpClient   *http.Client
	authURL      string // e.g. "https://auth.frontierstore.net"
	capiURL      string // e.g. "https://companion.orerve.net"
	clientID     string
	clientSecret string
	scope        string        // "auth capi"
	capiTimeout  time.Duration
}

// New creates a Frontier client. authURL and capiURL must not have trailing slashes.
func New(authURL, capiURL, clientID, clientSecret, scope string, capiTimeout time.Duration) *Client {
	return &Client{
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		authURL:      authURL,
		capiURL:      capiURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        scope,
		capiTimeout:  capiTimeout,
	}
}

// ExchangeCode exchanges an authorization code for tokens using PKCE.
// The request body MUST include scope=auth+capi (URL-encoded "auth capi").
func (c *Client) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI string) (*TokenResponse, error) {
	body := url.Values{}
	body.Set("grant_type", "authorization_code")
	body.Set("code", code)
	body.Set("code_verifier", codeVerifier)
	body.Set("redirect_uri", redirectURI)
	body.Set("client_id", c.clientID)
	body.Set("client_secret", c.clientSecret)
	body.Set("scope", c.scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.authURL+"/token", strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("frontier: creating exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frontier: exchange code request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("frontier: exchange code returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("frontier: decoding token response: %w", err)
	}

	return &tokenResp, nil
}

// GetMe calls /me to retrieve the customer_id.
func (c *Client) GetMe(ctx context.Context, accessToken string) (*MeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.authURL+"/me", nil)
	if err != nil {
		return nil, fmt.Errorf("frontier: creating /me request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frontier: /me request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("frontier: /me returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var meResp MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&meResp); err != nil {
		return nil, fmt.Errorf("frontier: decoding /me response: %w", err)
	}

	return &meResp, nil
}

// GetProfile calls CAPI /profile to retrieve the commander name.
// Uses a timeout of c.capiTimeout. Returns an error if CAPI is unavailable or times out.
func (c *Client) GetProfile(ctx context.Context, accessToken string) (*ProfileResponse, error) {
	capiCtx, cancel := context.WithTimeout(ctx, c.capiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(capiCtx, http.MethodGet, c.capiURL+"/profile", nil)
	if err != nil {
		return nil, fmt.Errorf("frontier: creating CAPI /profile request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Use a separate client without its own timeout so capiCtx controls it.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frontier: CAPI /profile request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("frontier: CAPI /profile returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var profileResp ProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profileResp); err != nil {
		return nil, fmt.Errorf("frontier: decoding CAPI /profile response: %w", err)
	}

	return &profileResp, nil
}

// RefreshToken exchanges a Frontier refresh token for new tokens.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	body := url.Values{}
	body.Set("grant_type", "refresh_token")
	body.Set("refresh_token", refreshToken)
	body.Set("client_id", c.clientID)
	body.Set("client_secret", c.clientSecret)
	body.Set("scope", c.scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.authURL+"/token", strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("frontier: creating refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frontier: refresh token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("frontier: refresh token returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("frontier: decoding refresh token response: %w", err)
	}

	return &tokenResp, nil
}
