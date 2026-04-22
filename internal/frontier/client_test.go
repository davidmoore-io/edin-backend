package frontier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient returns a Client pointed at authServer and capiServer.
func newTestClient(authURL, capiURL string, capiTimeout time.Duration) *Client {
	return New(authURL, capiURL, "test-client-id", "test-secret", "auth capi", capiTimeout)
}

func TestFrontierClient_ExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "access-123",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "refresh-456",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	resp, err := client.ExchangeCode(context.Background(), "authcode", "verifier", "https://example.com/cb")
	require.NoError(t, err)
	assert.Equal(t, "access-123", resp.AccessToken)
	assert.Equal(t, "refresh-456", resp.RefreshToken)
	assert.Equal(t, 3600, resp.ExpiresIn)
}

func TestFrontierClient_ExchangeCode_FrontierRejects_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	_, err := client.ExchangeCode(context.Background(), "bad-code", "verifier", "https://example.com/cb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestFrontierClient_ExchangeCode_ScopeInRequest(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			capturedBody = r.Form.Encode()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "tok",
			ExpiresIn:   3600,
		})
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	_, err := client.ExchangeCode(context.Background(), "code", "verifier", "https://example.com/cb")
	require.NoError(t, err)

	// Verify scope is present in the POST body.
	// url.ParseQuery will decode "auth+capi" or "auth%20capi" back to "auth capi".
	vals, err := url.ParseQuery(capturedBody)
	require.NoError(t, err)
	assert.Equal(t, "auth capi", vals.Get("scope"), "scope field must be 'auth capi'")
}

func TestFrontierClient_GetMe_ExtractsFID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me" || r.Method != http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		assert.Equal(t, "Bearer mytoken", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(MeResponse{CustomerID: "2504"})
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	resp, err := client.GetMe(context.Background(), "mytoken")
	require.NoError(t, err)
	assert.Equal(t, "2504", resp.CustomerID)
}

func TestFrontierClient_GetProfile_ExtractsCommanderName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/profile" || r.Method != http.MethodGet {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		assert.Equal(t, "Bearer capitoken", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		// Write raw JSON for nested struct
		w.Write([]byte(`{"commander":{"name":"Pattern State"}}`))
	}))
	defer srv.Close()

	client := newTestClient("http://auth.invalid", srv.URL, 5*time.Second)
	resp, err := client.GetProfile(context.Background(), "capitoken")
	require.NoError(t, err)
	assert.Equal(t, "Pattern State", resp.Commander.Name)
}

func TestFrontierClient_GetProfile_Timeout_ReturnsError(t *testing.T) {
	// Mock server that delays longer than capiTimeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"commander":{"name":"Slow"}}`))
	}))
	defer srv.Close()

	// Use a very short timeout so the request times out.
	client := newTestClient("http://auth.invalid", srv.URL, 50*time.Millisecond)
	_, err := client.GetProfile(context.Background(), "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CAPI /profile request")
}

func TestFrontierClient_RefreshToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		assert.Equal(t, "oldrefresh", r.Form.Get("refresh_token"))
		// Verify scope included in refresh too
		assert.Equal(t, "auth capi", r.Form.Get("scope"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new-access",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "new-refresh",
		})
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	resp, err := client.RefreshToken(context.Background(), "oldrefresh")
	require.NoError(t, err)
	assert.Equal(t, "new-access", resp.AccessToken)
	assert.Equal(t, "new-refresh", resp.RefreshToken)
}

// Ensure the scope string "auth capi" when URL-encoded contains "auth" and "capi".
func TestFrontierClient_ExchangeCode_ScopeURLEncoded(t *testing.T) {
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes := make([]byte, 4096)
		n, _ := r.Body.Read(bodyBytes)
		rawBody = string(bodyBytes[:n])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "tok", ExpiresIn: 3600})
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "http://capi.invalid", 5*time.Second)
	_, err := client.ExchangeCode(context.Background(), "code", "verifier", "https://example.com/cb")
	require.NoError(t, err)

	// The raw body should contain either "auth+capi" or "auth%20capi" as the encoded scope value.
	hasScope := strings.Contains(rawBody, "auth+capi") || strings.Contains(rawBody, "auth%20capi")
	assert.True(t, hasScope, "raw request body should contain URL-encoded 'auth capi' scope; got: %s", rawBody)
}
