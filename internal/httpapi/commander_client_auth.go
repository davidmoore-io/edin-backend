package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/edin-space/edin-backend/internal/frontier"
	"github.com/redis/go-redis/v9"
)

// ─── Redis key schema ─────────────────────────────────────────────────────────

const (
	clientAuthSessionKeyPrefix = "edin:client_auth_session:"
	clientAuthStateKeyPrefix   = "edin:client_auth_state:"

	clientAuthSessionPendingTTL  = 10 * time.Minute
	clientAuthSessionCompleteTTL = 5 * time.Minute
)

func clientAuthSessionKey(sessionID string) string {
	return clientAuthSessionKeyPrefix + sessionID
}

func clientAuthStateKey(state string) string {
	return clientAuthStateKeyPrefix + state
}

// clientAuthSession is stored in Redis under clientAuthSessionKey(sessionID).
type clientAuthSession struct {
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	Status       string `json:"status"`    // "pending" | "complete"
	Token        string `json:"token"`     // EDIN JWT (only when complete)
	ExpiresAt    string `json:"expires_at"` // RFC3339
}

// ─── Desktop flow redirect URI ────────────────────────────────────────────────

// desktopRedirectURI returns the FDEV-registered callback URL for the desktop poll flow.
// Configurable via DESKTOP_REDIRECT_URI env var; defaults to production URL.
func (s *Server) desktopRedirectURI() string {
	return s.cfg.CommanderAuth.DesktopRedirectURI
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleClientAuthInitiate handles POST /api/v1/auth/frontier/initiate.
// Returns {auth_url, session_id} for the desktop poll-based PKCE flow.
func (s *Server) handleClientAuthInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	if s.redisClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "session store not available")
		return
	}

	cfg := s.cfg.CommanderAuth
	ctx := r.Context()

	// Generate PKCE pair.
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate PKCE verifier")
		return
	}
	challenge := codeChallenge(codeVerifier)

	// Generate session_id and state (both crypto-random UUIDs).
	sessionID, err := generateStateUUID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate session ID")
		return
	}
	state, err := generateStateUUID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}

	// Store the session record.
	expiresAt := time.Now().Add(clientAuthSessionPendingTTL)
	session := clientAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		Status:       "pending",
		ExpiresAt:    expiresAt.Format(time.RFC3339),
	}
	if err := storeClientAuthSession(ctx, s.redisClient, sessionID, session, clientAuthSessionPendingTTL); err != nil {
		slog.Error("client_auth_initiate: failed to store session", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to store session")
		return
	}

	// Store reverse lookup: state → session_id.
	if err := s.redisClient.Set(ctx, clientAuthStateKey(state), sessionID, clientAuthSessionPendingTTL).Err(); err != nil {
		slog.Error("client_auth_initiate: failed to store state reverse lookup", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to store session")
		return
	}

	// Build Frontier auth URL using the fixed registered redirect_uri.
	authURL := cfg.FrontierAuthURL + "/auth" +
		"?client_id=" + cfg.FrontierClientID +
		"&response_type=code" +
		"&redirect_uri=" + urlEncode(s.desktopRedirectURI()) +
		"&scope=auth+capi" +
		"&state=" + state +
		"&code_challenge=" + challenge +
		"&code_challenge_method=S256"

	s.writeJSON(w, http.StatusOK, map[string]string{
		"auth_url":   authURL,
		"session_id": sessionID,
	})
}

// handleClientAuthPoll handles GET /api/v1/auth/frontier/poll?session_id=...
// Returns 202 (pending), 200+token (complete, single-use), or 410 (expired/not found).
func (s *Server) handleClientAuthPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.redisClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "session store not available")
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		s.writeError(w, http.StatusBadRequest, "missing session_id parameter")
		return
	}

	ctx := r.Context()

	raw, err := s.redisClient.Get(ctx, clientAuthSessionKey(sessionID)).Result()
	if err == redis.Nil {
		// Not found or expired — return 410 Gone.
		s.writeJSON(w, http.StatusGone, map[string]string{"status": "expired"})
		return
	}
	if err != nil {
		slog.Error("client_auth_poll: redis get failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "session lookup failed")
		return
	}

	var session clientAuthSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		slog.Error("client_auth_poll: unmarshal session", "error", err)
		s.writeError(w, http.StatusInternalServerError, "session data corrupted")
		return
	}

	switch session.Status {
	case "pending":
		s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
	case "complete":
		// Single-use: delete immediately before responding.
		if err := s.redisClient.Del(ctx, clientAuthSessionKey(sessionID)).Err(); err != nil {
			slog.Error("client_auth_poll: failed to delete completed session", "error", err)
			// Non-fatal — still return the token.
		}
		s.writeJSON(w, http.StatusOK, map[string]string{
			"status": "complete",
			"token":  session.Token,
		})
	default:
		slog.Error("client_auth_poll: unknown session status", "status", session.Status)
		s.writeError(w, http.StatusInternalServerError, "invalid session state")
	}
}

// handleClientAuthRefresh handles POST /api/v1/auth/refresh.
// Thin wrapper that delegates to handleCommanderAuthRefresh.
// The desktop client sends its JWT as "Authorization: Bearer <token>".
// This handler reads the Bearer header and synthesises a cookie so the
// shared refresh logic can process it transparently.
func (s *Server) handleClientAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	// If no cookie is present, try to promote the Bearer token to one.
	_, cookieErr := r.Cookie(cfg.CookieName)
	if cookieErr != nil {
		// Try Bearer header.
		authHeader := r.Header.Get("Authorization")
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			bearerToken := authHeader[7:]
			// Attach as a synthetic cookie so handleCommanderAuthRefresh can read it.
			r.AddCookie(&http.Cookie{
				Name:  cfg.CookieName,
				Value: bearerToken,
			})
		}
		// If no Bearer either, handleCommanderAuthRefresh will return 401.
	}

	// Change method to GET because handleCommanderAuthRefresh checks for GET.
	r.Method = http.MethodGet
	s.handleCommanderAuthRefresh(w, r)
}

// ─── Desktop callback path in handleCommanderAuthCallback ────────────────────

// lookupClientAuthSessionByState resolves a state value to (sessionID, session, true)
// by reading the reverse-lookup key from Redis.
// Returns ("", clientAuthSession{}, false) if not found.
func lookupClientAuthSessionByState(ctx context.Context, rdb *redis.Client, state string) (string, clientAuthSession, bool) {
	sessionID, err := rdb.Get(ctx, clientAuthStateKey(state)).Result()
	if err != nil {
		return "", clientAuthSession{}, false
	}

	raw, err := rdb.Get(ctx, clientAuthSessionKey(sessionID)).Result()
	if err != nil {
		return "", clientAuthSession{}, false
	}

	var session clientAuthSession
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return "", clientAuthSession{}, false
	}

	return sessionID, session, true
}

// completeClientAuthSession updates the session to status=complete with the EDIN JWT,
// resets the TTL to clientAuthSessionCompleteTTL, and deletes the state reverse-lookup.
func completeClientAuthSession(ctx context.Context, rdb *redis.Client, sessionID, state, token string) error {
	session := clientAuthSession{
		Status:    "complete",
		Token:     token,
		ExpiresAt: time.Now().Add(clientAuthSessionCompleteTTL).Format(time.RFC3339),
	}
	if err := storeClientAuthSession(ctx, rdb, sessionID, session, clientAuthSessionCompleteTTL); err != nil {
		return fmt.Errorf("completeClientAuthSession: store: %w", err)
	}
	// Best-effort delete of the state reverse-lookup (already consumed).
	_ = rdb.Del(ctx, clientAuthStateKey(state)).Err()
	return nil
}

// storeClientAuthSession marshals and writes a clientAuthSession to Redis.
func storeClientAuthSession(ctx context.Context, rdb *redis.Client, sessionID string, session clientAuthSession, ttl time.Duration) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("storeClientAuthSession: marshal: %w", err)
	}
	return rdb.Set(ctx, clientAuthSessionKey(sessionID), data, ttl).Err()
}

// handleClientAuthDesktopCallback is called from handleCommanderAuthCallback when
// the state belongs to a desktop flow session (found in Redis, not in commanderPKCEStore).
// It completes the token exchange, issues an EDIN JWT, stores it in the session, and
// returns a plain HTML page the browser tab can display.
func (s *Server) handleClientAuthDesktopCallback(w http.ResponseWriter, r *http.Request, sessionID string, session clientAuthSession, code string) {
	cfg := s.cfg.CommanderAuth
	ctx := r.Context()

	fc := frontier.New(
		cfg.FrontierAuthURL,
		cfg.FrontierCAPIURL,
		cfg.FrontierClientID,
		cfg.FrontierClientSecret,
		cfg.FrontierScope,
		cfg.FrontierCAPITimeout,
	)

	// Exchange code for tokens.
	tokenResp, err := fc.ExchangeCode(ctx, code, session.CodeVerifier, s.desktopRedirectURI())
	if err != nil {
		slog.Error("client_auth_callback: frontier exchange code failed", "error", err)
		s.writeError(w, http.StatusBadGateway, "failed to exchange authorization code")
		return
	}

	// Get FID from /me.
	meResp, err := fc.GetMe(ctx, tokenResp.AccessToken)
	if err != nil {
		slog.Error("client_auth_callback: frontier /me failed", "error", err)
		s.writeError(w, http.StatusBadGateway, "failed to retrieve frontier identity")
		return
	}
	fid := "F" + meResp.CustomerID

	// Get commander name from CAPI /profile — graceful fallback.
	name := "Unknown Commander"
	capiPending := false
	profileResp, err := fc.GetProfile(ctx, tokenResp.AccessToken)
	if err != nil {
		slog.Warn("client_auth_callback: CAPI /profile failed, setting capi_pending=true", "error", err)
		capiPending = true
	} else if profileResp.Commander.Name != "" {
		name = profileResp.Commander.Name
	} else {
		capiPending = true
	}

	// Issue EDIN JWT.
	if s.commanderJWTIssuer == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	tokenString, jti, err := s.commanderJWTIssuer.Issue(fid, name)
	if err != nil {
		slog.Error("client_auth_callback: JWT issue failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to issue session token")
		return
	}

	// Store Frontier tokens in Redis.
	if s.redisClient != nil {
		if err := storeFrontierTokens(ctx, s.redisClient, jti, tokenResp, capiPending); err != nil {
			slog.Error("client_auth_callback: failed to store frontier tokens", "error", err)
			// Non-fatal.
		}

		// Mark session as complete with the EDIN JWT.
		if err := completeClientAuthSession(ctx, s.redisClient, sessionID, session.State, tokenString); err != nil {
			slog.Error("client_auth_callback: failed to complete session", "error", err)
			// Non-fatal — desktop will poll and get 410, user will need to retry.
		}
	}

	// Return a simple HTML page so the browser tab shows something useful.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>EDIN Authentication</title></head>
<body>
<h2>Authentication successful!</h2>
<p>You can close this window and return to the EDIN desktop app.</p>
</body>
</html>`)
}

// RegisterClientAuthRoutes registers the desktop-client poll-based auth routes.
func (s *Server) RegisterClientAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/frontier/initiate", s.handleClientAuthInitiate)
	mux.HandleFunc("/api/v1/auth/frontier/poll", s.handleClientAuthPoll)
	mux.HandleFunc("/api/v1/auth/refresh", s.handleClientAuthRefresh)
	mux.Handle("GET /api/v1/auth/me", s.withCommanderAuth(http.HandlerFunc(s.handleAuthMe)))
}
