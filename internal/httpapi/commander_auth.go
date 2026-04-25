package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/frontier"
	"github.com/edin-space/edin-backend/internal/security"
	"github.com/redis/go-redis/v9"
)

// ─── PKCE store ──────────────────────────────────────────────────────────────

// commanderPKCEEntry holds a PKCE code verifier, the redirect URI used during
// initiation, and a TTL. The redirect URI must be preserved so the token
// exchange uses exactly the same value that was sent to Frontier.
type commanderPKCEEntry struct {
	codeVerifier string
	redirectURI  string
	expiresAt    time.Time
}

// commanderPKCEStore is an in-memory PKCE state store backed by sync.Map.
// Key = state UUID (string), Value = commanderPKCEEntry.
// Limits pending auth sessions to PKCEMaxPending.
type commanderPKCEStore struct {
	items   sync.Map
	count   atomic.Int64
	maxPend int
}

func newCommanderPKCEStore(maxPending int) *commanderPKCEStore {
	s := &commanderPKCEStore{maxPend: maxPending}
	go s.cleanup()
	return s
}

// store adds a new PKCE entry. Returns false if the store is full.
func (s *commanderPKCEStore) store(state, codeVerifier, redirectURI string, ttl time.Duration) bool {
	if s.count.Load() >= int64(s.maxPend) {
		return false
	}
	entry := commanderPKCEEntry{
		codeVerifier: codeVerifier,
		redirectURI:  redirectURI,
		expiresAt:    time.Now().Add(ttl),
	}
	s.items.Store(state, entry)
	s.count.Add(1)
	return true
}

// consume retrieves and deletes the PKCE entry.
// Returns ("", "", false) if not found or expired.
func (s *commanderPKCEStore) consume(state string) (codeVerifier, redirectURI string, ok bool) {
	v, found := s.items.LoadAndDelete(state)
	if !found {
		return "", "", false
	}
	s.count.Add(-1)
	entry := v.(commanderPKCEEntry)
	if time.Now().After(entry.expiresAt) {
		return "", "", false
	}
	return entry.codeVerifier, entry.redirectURI, true
}

// cleanup runs every 5 minutes and removes expired entries.
func (s *commanderPKCEStore) cleanup() {
	for {
		time.Sleep(5 * time.Minute)
		now := time.Now()
		s.items.Range(func(k, v any) bool {
			entry := v.(commanderPKCEEntry)
			if now.After(entry.expiresAt) {
				s.items.Delete(k)
				s.count.Add(-1)
			}
			return true
		})
	}
}

// ─── Redis token storage ─────────────────────────────────────────────────────

// frontierTokenRecord is stored in Redis keyed by "edin:frontier_token:{jti}".
type frontierTokenRecord struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"` // RFC3339
	CAPIPending  bool   `json:"capi_pending"`
}

const frontierTokenKeyPrefix = "edin:frontier_token:"

func frontierTokenKey(jti string) string {
	return frontierTokenKeyPrefix + jti
}

// storeFrontierTokens writes Frontier tokens to Redis.
func storeFrontierTokens(ctx context.Context, rdb *redis.Client, jti string, tokenResp *frontier.TokenResponse, capiPending bool) error {
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	record := frontierTokenRecord{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		CAPIPending:  capiPending,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("commander_auth: marshalling frontier token: %w", err)
	}
	ttl := time.Duration(tokenResp.ExpiresIn) * time.Second
	return rdb.Set(ctx, frontierTokenKey(jti), data, ttl).Err()
}

// ─── Per-IP rate limiter ──────────────────────────────────────────────────────

// getOrCreateIPLimiter retrieves or creates a per-IP TokenBucket with the given rate and window.
func getOrCreateIPLimiter(store *sync.Map, ip string, rate int, window time.Duration) *security.TokenBucket {
	v, _ := store.LoadOrStore(ip, security.NewTokenBucket(rate, window))
	return v.(*security.TokenBucket)
}

// ─── PKCE helpers ─────────────────────────────────────────────────────────────

// generateCodeVerifier generates a 43-char base64url random code verifier.
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32) // 32 bytes → 43-char base64url (without padding)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("commander_auth: generating code verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallenge computes SHA256(verifier) base64url-encoded.
func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateStateUUID generates a UUID-format state string using crypto/rand.
func generateStateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("commander_auth: generating state UUID: %w", err)
	}
	// Format as UUID: 8-4-4-4-12
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ─── redirectURI helper ───────────────────────────────────────────────────────

// buildRedirectURIPath reconstructs a redirect URI from the request for the
// given path. Uses X-Forwarded-Proto if set (Caddy sets this), else falls back
// to "http" for plain connections or "https" for TLS.
func buildRedirectURIPath(r *http.Request, path string) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host + path
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleCommanderAuthInitiate handles GET /api/commander/auth/initiate.
func (s *Server) handleCommanderAuthInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	// Per-IP rate limiting.
	ip := clientIP(r)
	limiter := getOrCreateIPLimiter(&s.commanderIPLimiter, ip, cfg.InitiateRateLimit, cfg.InitiateRateWindow)
	if !limiter.Allow() {
		s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Generate PKCE verifier and challenge.
	verifier, err := generateCodeVerifier()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate PKCE verifier")
		return
	}
	challenge := codeChallenge(verifier)

	// Generate state UUID.
	state, err := generateStateUUID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}

	// Build redirect URI — must point to the frontend /copilot/callback page so
	// Frontier lands the browser on the React app, which reads code+state from
	// the URL and calls the backend exchange endpoint via fetch.
	redirectURI := buildRedirectURIPath(r, "/copilot/callback")

	// Store in PKCE store alongside the redirect URI for the token exchange.
	if !s.commanderPKCEStore.store(state, verifier, redirectURI, cfg.PKCEStateTTL) {
		s.writeError(w, http.StatusServiceUnavailable, "too many pending auth sessions")
		return
	}

	// Build Frontier auth URL.
	authURL := cfg.FrontierAuthURL + "/auth" +
		"?client_id=" + cfg.FrontierClientID +
		"&response_type=code" +
		"&redirect_uri=" + urlEncode(redirectURI) +
		"&scope=auth+capi" +
		"&state=" + state +
		"&code_challenge=" + challenge +
		"&code_challenge_method=S256"

	s.writeJSON(w, http.StatusOK, map[string]string{
		"auth_url": authURL,
	})
}

// handleCommanderAuthCallback handles GET /api/commander/auth/callback?code=...&state=...
func (s *Server) handleCommanderAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		s.writeError(w, http.StatusBadRequest, "missing code or state parameter")
		return
	}

	// Look up and delete PKCE state — try browser flow first, then desktop flow.
	// The stored redirectURI must be used for the Frontier token exchange; it
	// must exactly match the value sent in the original auth request.
	codeVerifier, redirectURI, ok := s.commanderPKCEStore.consume(state)
	if !ok {
		// Not a browser session — check if this is a desktop poll-based session.
		if s.redisClient != nil {
			sessionID, session, found := lookupClientAuthSessionByState(r.Context(), s.redisClient, state)
			if found {
				s.handleClientAuthDesktopCallback(w, r, sessionID, session, code)
				return
			}
		}
		s.writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	// Build frontier client.
	fc := frontier.New(
		cfg.FrontierAuthURL,
		cfg.FrontierCAPIURL,
		cfg.FrontierClientID,
		cfg.FrontierClientSecret,
		cfg.FrontierScope,
		cfg.FrontierCAPITimeout,
	)

	am := initEdinMetrics()

	// Exchange code for tokens.
	tokenResp, err := fc.ExchangeCode(r.Context(), code, codeVerifier, redirectURI)
	if err != nil {
		slog.Error("frontier exchange code failed", "error", err)
		am.commanderAuthAttemptsTotal.WithLabelValues("failure").Inc()
		s.writeError(w, http.StatusBadGateway, "failed to exchange authorization code")
		return
	}

	// Get FID from /me endpoint.
	meResp, err := fc.GetMe(r.Context(), tokenResp.AccessToken)
	if err != nil {
		slog.Error("frontier /me failed", "error", err)
		am.commanderAuthAttemptsTotal.WithLabelValues("failure").Inc()
		s.writeError(w, http.StatusBadGateway, "failed to retrieve frontier identity")
		return
	}
	fid := "F" + meResp.CustomerID

	// Get commander name from CAPI /profile — graceful fallback on error/timeout.
	name := "Unknown Commander"
	capiPending := false
	profileResp, err := fc.GetProfile(r.Context(), tokenResp.AccessToken)
	if err != nil {
		slog.Warn("CAPI /profile failed, setting capi_pending=true", "error", err)
		capiPending = true
	} else {
		if profileResp.Commander.Name != "" {
			name = profileResp.Commander.Name
		} else {
			// Profile returned but no name — treat as pending.
			capiPending = true
		}
	}

	// Record (or refresh) the commander row before any link / allowlist
	// resolution. Required: ensureCommanderLink reads the row via
	// GetCommanderAsAdmin and expects it to exist. The platform sentinel
	// "frontier" means "Frontier-OAuth-confirmed, in-game platform unknown
	// until the next journal ingest"; ingest's UpsertCommander will
	// overwrite once a real platform string arrives.
	if s.commanderRepo != nil {
		if _, err := s.commanderRepo.UpsertCommander(r.Context(), fid, name, "frontier"); err != nil {
			slog.Error("commander_callback_upsert_failed", "fid", fid, "err", err)
			am.commanderAuthAttemptsTotal.WithLabelValues("failure").Inc()
			s.writeError(w, http.StatusInternalServerError, "failed to record commander")
			return
		}

		// Auto-link to a shadow Authentik user on first login. Best-effort
		// idempotent on duplicate-username (handled inside CreateShadowUser).
		// Any other Authentik error is deny-closed: audit + 403, do not
		// fall through to JWT issuance. Behaviour is symmetric across the
		// web and desktop callbacks.
		if _, err := s.ensureCommanderLink(r.Context(), fid, name); err != nil {
			reason := "authentik_unreachable"
			if errors.Is(err, errLinkPersistFailed) {
				reason = "link_persist_failed"
			}
			slog.Error("commander_link_failed",
				"fid", fid,
				"flow", string(loginFlowWeb),
				"reason", reason,
				"err", err,
			)
			s.recordDeniedLogin(deniedLoginAttempt{
				Time:          time.Now().UTC(),
				FID:           fid,
				CommanderName: name,
				Flow:          loginFlowWeb,
				IP:            clientIP(r),
				UserAgent:     r.UserAgent(),
				Reason:        reason,
			})
			am.commanderAuthAttemptsTotal.WithLabelValues("denied").Inc()
			s.writeError(w, http.StatusForbidden,
				"this commander is not currently permitted to use EDIN. Contact the administrator to request access.")
			return
		}
	}

	// Resolve commander access — single decision-point that consults the
	// linked Authentik user's groups for approved commanders, falls back to
	// the env-var allowlist for transitional cases, and denies otherwise.
	// See commander_allowlist.go for the full decision matrix.
	decision := s.resolveCommanderAccess(r.Context(), r, loginFlowWeb, fid, name)
	if !decision.Allowed {
		if decision.Denial != nil {
			s.recordDeniedLogin(*decision.Denial)
		}
		am.commanderAuthAttemptsTotal.WithLabelValues("denied").Inc()
		s.writeError(w, http.StatusForbidden,
			"this commander is not currently permitted to use EDIN. Contact the administrator to request access.")
		return
	}

	// Issue EDIN JWT.
	if s.commanderJWTIssuer == nil {
		am.commanderAuthAttemptsTotal.WithLabelValues("failure").Inc()
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	// Scopes derived by resolveCommanderAccess — either from Authentik group
	// membership (linked + approved commanders) or the default commander set
	// (allowlist-fallback transitional case).
	tokenString, jti, err := s.commanderJWTIssuer.Issue(fid, name, decision.Scopes)
	if err != nil {
		slog.Error("JWT issue failed", "error", err)
		am.commanderAuthAttemptsTotal.WithLabelValues("failure").Inc()
		s.writeError(w, http.StatusInternalServerError, "failed to issue session token")
		return
	}

	// Track the jti under the per-FID set so Task 8 admin actions (Deny/Unlink)
	// can enumerate and revoke every live JWT for this commander. Best-effort:
	// a failure here means we lose instant-revoke for this jti, but the JWT
	// still expires naturally at its normal TTL.
	if s.redisClient != nil {
		if err := s.redisClient.SAdd(r.Context(), "commander:jtis:"+fid, jti).Err(); err != nil {
			slog.Warn("commander_jti_track_failed", "fid", fid, "err", err)
		}
		_ = s.redisClient.Expire(r.Context(), "commander:jtis:"+fid, 24*time.Hour).Err()
	}

	// Store Frontier tokens in Redis.
	if s.redisClient != nil {
		if err := storeFrontierTokens(r.Context(), s.redisClient, jti, tokenResp, capiPending); err != nil {
			slog.Error("failed to store frontier tokens in Redis", "error", err)
			// Non-fatal — auth can still succeed without Redis storage.
		}
	}

	// Set httpOnly cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    tokenString,
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		MaxAge:   cfg.CookieMaxAge,
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	// Record auth outcome: success or capi_pending.
	if capiPending {
		am.commanderAuthAttemptsTotal.WithLabelValues("capi_pending").Inc()
	} else {
		am.commanderAuthAttemptsTotal.WithLabelValues("success").Inc()
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"commander_name": name,
		"fid":            fid,
		"capi_pending":   capiPending,
	})
}

// handleCommanderAuthStatus handles GET /api/commander/auth/status.
// Public endpoint — returns the authentication state from the commander_session cookie.
func (s *Server) handleCommanderAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	// Read commander_session cookie.
	cookie, err := r.Cookie(cfg.CookieName)
	if err != nil {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"authenticated": false,
		})
		return
	}

	// Validate JWT.
	if s.commanderJWTValidator == nil {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"authenticated": false,
		})
		return
	}
	claims, err := s.commanderJWTValidator.Validate(r.Context(), cookie.Value)
	if err != nil {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{
			"authenticated": false,
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":  true,
		"commander_name": claims.Name,
		"fid":            claims.FID,
	})
}

// handleCommanderAuthToken handles GET /api/commander/auth/token.
// Public endpoint — CSRF-protected via X-Edin-Fetch header.
// Validates the commander_session cookie and issues a single-use nonce.
func (s *Server) handleCommanderAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	// CSRF guard: browsers won't set this header for cross-site requests.
	if r.Header.Get("X-Edin-Fetch") != "1" {
		s.writeError(w, http.StatusForbidden, "missing X-Edin-Fetch header")
		return
	}

	cfg := s.cfg.CommanderAuth

	// Read commander_session cookie.
	cookie, err := r.Cookie(cfg.CookieName)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "no session cookie")
		return
	}

	// Validate JWT.
	if s.commanderJWTValidator == nil {
		s.writeError(w, http.StatusUnauthorized, "commander auth not configured")
		return
	}
	claims, err := s.commanderJWTValidator.Validate(r.Context(), cookie.Value)
	if err != nil {
		slog.Warn("commander_token: JWT validation failed", "error", err)
		s.writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	// Issue single-use nonce for the copilot WebSocket auth frame. The JWT was
	// just validated above, so any consumer of this nonce is a fully-authenticated
	// commander — no secondary role check is performed on the WS side.
	//
	// Scopes are the default commander set for now — Task 6 in the
	// Authentik commander access plan replaces this hardcode with scopes
	// drawn from the JWT "scopes" claim. Until then every verified
	// commander receives the same scope set, matching today's behaviour
	// before this task.
	user := &CommanderChatUser{
		FID:  claims.FID,
		Name: claims.Name,
		Scopes: []authz.Scope{
			authz.ScopeCopilotChat,
			authz.ScopeGalaxyRead,
			authz.ScopeCommanderData,
		},
	}
	nonce := s.commanderNonceStore.Issue(user, cfg.NonceExpiry)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"nonce":      nonce,
		"expires_in": int(cfg.NonceExpiry.Seconds()),
	})
}

// handleCommanderAuthLogout handles POST /api/commander/auth/logout.
func (s *Server) handleCommanderAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	// Read session cookie.
	cookie, err := r.Cookie(cfg.CookieName)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "missing session cookie")
		return
	}

	// Validate JWT.
	if s.commanderJWTValidator == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	claims, err := s.commanderJWTValidator.Validate(r.Context(), cookie.Value)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "invalid session token")
		return
	}

	// Revoke JTI.
	if err := s.commanderJWTValidator.RevokeJTI(r.Context(), claims.ID, claims.ExpiresAt.Time); err != nil {
		slog.Error("failed to revoke JTI", "jti", claims.ID, "error", err)
		// Non-fatal — clear cookie anyway.
	}

	// Drop this jti from the per-FID tracking set so the set stays accurate.
	// Best-effort — a failure here just leaves a stale member that will expire
	// naturally with the 24h TTL on the set.
	if s.redisClient != nil {
		if err := s.redisClient.SRem(r.Context(), "commander:jtis:"+claims.FID, claims.ID).Err(); err != nil {
			slog.Warn("commander_jti_untrack_failed", "fid", claims.FID, "err", err)
		}
	}

	// Clear cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     cfg.CookiePath,
		MaxAge:   -1,
		HttpOnly: true,
	})

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "logged_out",
	})
}

// handleCommanderAuthRefresh handles GET /api/commander/auth/refresh.
// It validates the existing session, optionally refreshes Frontier tokens,
// issues a new EDIN JWT, revokes the old one, and returns updated session info.
func (s *Server) handleCommanderAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	cfg := s.cfg.CommanderAuth
	ctx := r.Context()

	// Step 1: Read commander_session cookie.
	cookie, err := r.Cookie(cfg.CookieName)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "no session cookie")
		return
	}

	// Step 2: Validate EDIN JWT.
	if s.commanderJWTValidator == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	claims, err := s.commanderJWTValidator.Validate(ctx, cookie.Value)
	if err != nil {
		slog.Warn("commander_refresh: JWT validation failed", "error", err)
		s.writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	// Step 3: Extract JTI.
	oldJTI := claims.ID
	fid := claims.FID
	name := claims.Name

	// Step 4: Look up Frontier token record in Redis.
	if s.redisClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "redis not configured")
		return
	}
	raw, err := s.redisClient.Get(ctx, frontierTokenKey(oldJTI)).Result()
	if err != nil {
		if err == redis.Nil {
			s.writeError(w, http.StatusUnauthorized, "session expired, please re-authenticate")
			return
		}
		slog.Error("commander_refresh: redis get failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "session lookup failed")
		return
	}

	// Step 5: Parse stored token record.
	var record frontierTokenRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		slog.Error("commander_refresh: unmarshal frontier record", "error", err)
		s.writeError(w, http.StatusInternalServerError, "session data corrupted")
		return
	}

	// Step 6: Check if Frontier access token is still valid.
	expiresAt, err := time.Parse(time.RFC3339, record.ExpiresAt)
	if err != nil {
		slog.Error("commander_refresh: parse expires_at", "error", err)
		s.writeError(w, http.StatusInternalServerError, "session data corrupted")
		return
	}

	fc := frontier.New(
		cfg.FrontierAuthURL,
		cfg.FrontierCAPIURL,
		cfg.FrontierClientID,
		cfg.FrontierClientSecret,
		cfg.FrontierScope,
		cfg.FrontierCAPITimeout,
	)

	var newTokenResp *frontier.TokenResponse
	capiPending := record.CAPIPending

	if time.Now().After(expiresAt) {
		// Access token is expired — need to refresh.
		if record.RefreshToken == "" {
			s.writeError(w, http.StatusUnauthorized, "refresh token not available")
			return
		}
		newTokenResp, err = fc.RefreshToken(ctx, record.RefreshToken)
		if err != nil {
			slog.Error("commander_refresh: frontier refresh token failed", "error", err)
			s.writeError(w, http.StatusBadGateway, "failed to refresh frontier tokens")
			return
		}
	}
	// If not expired, newTokenResp remains nil — we'll reuse the existing record.

	// Step 7: If CAPI pending, attempt to resolve the commander name.
	if capiPending {
		accessToken := record.AccessToken
		if newTokenResp != nil {
			accessToken = newTokenResp.AccessToken
		}
		profileResp, err := fc.GetProfile(ctx, accessToken)
		if err != nil {
			slog.Warn("commander_refresh: CAPI profile retry failed, keeping capi_pending", "error", err)
			// Keep capiPending=true and existing name.
		} else if profileResp.Commander.Name != "" {
			name = profileResp.Commander.Name
			capiPending = false
		}
	}

	// Step 8: Issue new EDIN JWT.
	if s.commanderJWTIssuer == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	// Default commander scope set — same literal as the web and desktop
	// callbacks. Task 6 replaces this with resolveCommanderAccess.
	newTokenString, newJTI, err := s.commanderJWTIssuer.Issue(fid, name, []authz.Scope{
		authz.ScopeCopilotChat,
		authz.ScopeGalaxyRead,
		authz.ScopeCommanderData,
	})
	if err != nil {
		slog.Error("commander_refresh: JWT issue failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to issue session token")
		return
	}

	// Step 9: Revoke old JTI.
	if err := s.commanderJWTValidator.RevokeJTI(ctx, oldJTI, claims.ExpiresAt.Time); err != nil {
		slog.Error("commander_refresh: failed to revoke old JTI", "jti", oldJTI, "error", err)
		// Non-fatal — continue with the new token.
	}

	// Refresh rotates the jti — the old jti has already been revoked
	// via RevokeJTI above; update the per-FID tracking set so the
	// invariant (every active jti enumerable via SMEMBERS
	// commander:jtis:{fid}) holds. Order matters: SAdd the new jti
	// first so that a partial failure (SAdd succeeds, SRem fails)
	// leaves the new jti tracked — a stale old member is harmless
	// (revokeAllSessions would re-revoke an already-revoked token),
	// but an untracked active jti is NOT (revokeAllSessions would
	// miss it). Task 8's revokeAllSessions depends on this.
	if s.redisClient != nil {
		if err := s.redisClient.SAdd(ctx, "commander:jtis:"+fid, newJTI).Err(); err != nil {
			slog.Error("commander_jti_track_failed", "fid", fid, "new_jti", newJTI, "err", err)
		}
		if err := s.redisClient.SRem(ctx, "commander:jtis:"+fid, oldJTI).Err(); err != nil {
			slog.Warn("commander_jti_untrack_failed", "fid", fid, "old_jti", oldJTI, "err", err)
		}
		_ = s.redisClient.Expire(ctx, "commander:jtis:"+fid, 24*time.Hour).Err()
	}

	// Step 10: Store Frontier tokens under the new JTI.
	if newTokenResp != nil {
		// We got fresh Frontier tokens — store them.
		if err := storeFrontierTokens(ctx, s.redisClient, newJTI, newTokenResp, capiPending); err != nil {
			slog.Error("commander_refresh: failed to store new frontier tokens", "error", err)
			// Non-fatal.
		}
	} else {
		// Reuse existing record under new JTI with TTL based on remaining Frontier token life.
		updatedRecord := frontierTokenRecord{
			AccessToken:  record.AccessToken,
			RefreshToken: record.RefreshToken,
			ExpiresAt:    record.ExpiresAt,
			CAPIPending:  capiPending,
		}
		data, err := json.Marshal(updatedRecord)
		if err != nil {
			slog.Error("commander_refresh: marshal updated record", "error", err)
			// Non-fatal.
		} else {
			ttl := time.Until(expiresAt)
			if ttl < time.Minute {
				ttl = time.Minute // ensure a minimum useful TTL
			}
			if err := s.redisClient.Set(ctx, frontierTokenKey(newJTI), data, ttl).Err(); err != nil {
				slog.Error("commander_refresh: redis set new JTI record", "error", err)
				// Non-fatal.
			}
		}
	}

	// Step 11: Set new commander_session cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     cfg.CookieName,
		Value:    newTokenString,
		Path:     cfg.CookiePath,
		Domain:   cfg.CookieDomain,
		MaxAge:   cfg.CookieMaxAge,
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	// Step 12: Return updated session info.
	s.writeJSON(w, http.StatusOK, map[string]any{
		"commander_name": name,
		"fid":            fid,
		"capi_pending":   capiPending,
	})
}

// RegisterCommanderRoutes adds Commander auth API routes to the mux.
func (s *Server) RegisterCommanderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/commander/auth/initiate", s.handleCommanderAuthInitiate)
	mux.HandleFunc("/api/commander/auth/callback", s.handleCommanderAuthCallback)
	mux.HandleFunc("/api/commander/auth/logout", s.handleCommanderAuthLogout)
	mux.HandleFunc("/api/commander/auth/status", s.handleCommanderAuthStatus)
	mux.HandleFunc("/api/commander/auth/token", s.handleCommanderAuthToken)
	mux.HandleFunc("/api/commander/auth/refresh", s.handleCommanderAuthRefresh)
}

// urlEncode encodes a string for use in a URL query value.
func urlEncode(s string) string {
	// Use net/url for proper encoding.
	return (&urlValues{s}).Encode()
}

// urlValues is a helper to URL-encode a single value.
type urlValues struct {
	v string
}

func (u *urlValues) Encode() string {
	// Encode using standard library path escaping.
	var sb []byte
	for i := 0; i < len(u.v); i++ {
		c := u.v[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			sb = append(sb, c)
		case c == ' ':
			sb = append(sb, '+')
		default:
			sb = append(sb, '%', hexChar(c>>4), hexChar(c&0xf))
		}
	}
	return string(sb)
}

func hexChar(c byte) byte {
	if c < 10 {
		return '0' + c
	}
	return 'A' + c - 10
}
