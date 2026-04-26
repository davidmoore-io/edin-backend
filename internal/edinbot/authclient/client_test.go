package authclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/authclient"
	"github.com/stretchr/testify/require"
)

// fakeOAuthServer returns 1h-expiry tokens of the form "tok-N" where N
// increments with each request. Used for cache-behaviour tests.
type fakeOAuthServer struct {
	srv        *httptest.Server
	tokenCount atomic.Int64
	failNextN  atomic.Int64 // when > 0, returns 500 and decrements
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	f := &fakeOAuthServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/application/o/token/", func(w http.ResponseWriter, r *http.Request) {
		if f.failNextN.Load() > 0 {
			f.failNextN.Add(-1)
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		// Form-encoded body assertions (sanity).
		require.NoError(t, r.ParseForm())
		require.Equal(t, "client_credentials", r.Form.Get("grant_type"))

		n := f.tokenCount.Add(1)
		body := map[string]any{
			"access_token": "tok-" + strconv.FormatInt(n, 10),
			"token_type":   "bearer",
			"expires_in":   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestAuthClient_FetchTokenOnFirstCall(t *testing.T) {
	srv := newFakeOAuthServer(t)
	c := authclient.New(authclient.Config{
		TokenURL:     srv.srv.URL + "/application/o/token/",
		ClientID:     "cid",
		ClientSecret: "csec",
	})

	tok, err := c.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-1", tok)
	require.EqualValues(t, 1, srv.tokenCount.Load())
}

func TestAuthClient_CachesTokenWithinExpiry(t *testing.T) {
	srv := newFakeOAuthServer(t)
	c := authclient.New(authclient.Config{
		TokenURL:     srv.srv.URL + "/application/o/token/",
		ClientID:     "cid",
		ClientSecret: "csec",
	})

	tok1, err := c.Token(context.Background())
	require.NoError(t, err)
	tok2, err := c.Token(context.Background())
	require.NoError(t, err)

	require.Equal(t, tok1, tok2, "second call within expiry must reuse the cached token")
	require.EqualValues(t, 1, srv.tokenCount.Load(), "exactly one OAuth fetch")
}

func TestAuthClient_RefetchesAfterExpiry(t *testing.T) {
	srv := newFakeOAuthServer(t)
	now := time.Now()
	clock := &fakeClock{t: now}
	c := authclient.New(authclient.Config{
		TokenURL:        srv.srv.URL + "/application/o/token/",
		ClientID:        "cid",
		ClientSecret:    "csec",
		RefreshLeadTime: 30 * time.Second,
		Now:             clock.Now,
	})

	_, err := c.Token(context.Background())
	require.NoError(t, err)

	clock.advance(2 * time.Hour) // far past expiry

	tok, err := c.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-2", tok, "must mint a fresh token after expiry")
	require.EqualValues(t, 2, srv.tokenCount.Load())
}

func TestAuthClient_RefreshesEarlyWithinLeadTime(t *testing.T) {
	srv := newFakeOAuthServer(t)
	now := time.Now()
	clock := &fakeClock{t: now}
	c := authclient.New(authclient.Config{
		TokenURL:        srv.srv.URL + "/application/o/token/",
		ClientID:        "cid",
		ClientSecret:    "csec",
		RefreshLeadTime: 5 * time.Minute,
		Now:             clock.Now,
	})

	_, err := c.Token(context.Background())
	require.NoError(t, err)

	// Token expiry is 1h; advance to 56 min — leaves only 4 min, less than 5-min lead.
	clock.advance(56 * time.Minute)

	tok, err := c.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tok-2", tok, "must refresh early when within lead time of expiry")
}

func TestAuthClient_PropagatesFetchError(t *testing.T) {
	srv := newFakeOAuthServer(t)
	srv.failNextN.Store(1)

	c := authclient.New(authclient.Config{
		TokenURL:     srv.srv.URL + "/application/o/token/",
		ClientID:     "cid",
		ClientSecret: "csec",
	})

	_, err := c.Token(context.Background())
	require.Error(t, err, "transient OAuth failure must surface to caller")
}

func TestAuthClient_PropagatesEmptyTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"","token_type":"bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	c := authclient.New(authclient.Config{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		ClientSecret: "csec",
	})
	_, err := c.Token(context.Background())
	require.ErrorContains(t, err, "empty access_token")
}
