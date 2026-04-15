package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edin-space/edin-backend/internal/frontier"
	"github.com/edin-space/edin-backend/internal/security"
	"github.com/redis/go-redis/v9"
)

// ─── PKCE store ──────────────────────────────────────────────────────────────

// commanderPKCEEntry holds a PKCE code verifier with TTL.
type commanderPKCEEntry struct {
	codeVerifier string
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
func (s *commanderPKCEStore) store(state, codeVerifier string, ttl time.Duration) bool {
	if s.count.Load() >= int64(s.maxPend) {
		return false
	}
	entry := commanderPKCEEntry{
		codeVerifier: codeVerifier,
		expiresAt:    time.Now().Add(ttl),
	}
	s.items.Store(state, entry)
	s.count.Add(1)
	return true
}

// consume retrieves and deletes the PKCE entry. Returns empty string if not found or expired.
func (s *commanderPKCEStore) consume(state string) (string, bool) {
	v, ok := s.items.LoadAndDelete(state)
	if !ok {
		return "", false
	}
	s.count.Add(-1)
	entry := v.(commanderPKCEEntry)
	if time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.codeVerifier, true
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

// getOrCreateIPLimiter retrieves or creates a per-IP TokenBucket (5 req/min).
func getOrCreateIPLimiter(store *sync.Map, ip string) *security.TokenBucket {
	v, loaded := store.LoadOrStore(ip, security.NewTokenBucket(5, time.Minute))
	if loaded {
		return v.(*security.TokenBucket)
	}
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

// buildRedirectURI reconstructs the redirect URI from the request.
// Uses X-Forwarded-Proto if set (Caddy sets this), else falls back to "http".
func buildRedirectURI(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host + "/api/commander/auth/callback"
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleCommanderAuthInitiate handles GET /api/commander/auth/initiate.
func (s *Server) handleCommanderAuthInitiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	cfg := s.cfg.CommanderAuth

	// Per-IP rate limiting: 5 requests per minute.
	ip := clientIP(r)
	limiter := getOrCreateIPLimiter(&s.commanderIPLimiter, ip)
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

	// Store in PKCE store.
	if !s.commanderPKCEStore.store(state, verifier, cfg.PKCEStateTTL) {
		s.writeError(w, http.StatusServiceUnavailable, "too many pending auth sessions")
		return
	}

	// Build Frontier auth URL.
	authURL := cfg.FrontierAuthURL + "/auth" +
		"?client_id=" + cfg.FrontierClientID +
		"&response_type=code" +
		"&redirect_uri=" + urlEncode(buildRedirectURI(r)) +
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

	// Look up and delete PKCE state.
	codeVerifier, ok := s.commanderPKCEStore.consume(state)
	if !ok {
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

	redirectURI := buildRedirectURI(r)

	// Exchange code for tokens.
	tokenResp, err := fc.ExchangeCode(r.Context(), code, codeVerifier, redirectURI)
	if err != nil {
		slog.Error("frontier exchange code failed", "error", err)
		s.writeError(w, http.StatusBadGateway, "failed to exchange authorization code")
		return
	}

	// Get FID from /me endpoint.
	meResp, err := fc.GetMe(r.Context(), tokenResp.AccessToken)
	if err != nil {
		slog.Error("frontier /me failed", "error", err)
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

	// Issue EDIN JWT.
	if s.commanderJWTIssuer == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
		return
	}
	tokenString, jti, err := s.commanderJWTIssuer.Issue(fid, name)
	if err != nil {
		slog.Error("JWT issue failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to issue session token")
		return
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

	s.writeJSON(w, http.StatusOK, map[string]any{
		"commander_name": name,
		"fid":            fid,
		"capi_pending":   capiPending,
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

// RegisterCommanderRoutes adds Commander auth API routes to the mux.
func (s *Server) RegisterCommanderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/commander/auth/initiate", s.handleCommanderAuthInitiate)
	mux.HandleFunc("/api/commander/auth/callback", s.handleCommanderAuthCallback)
	mux.HandleFunc("/api/commander/auth/logout", s.handleCommanderAuthLogout)
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
