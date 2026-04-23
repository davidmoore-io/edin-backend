package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/authz"
)

// withCommanderAuth validates the commander_session cookie or Authorization: Bearer header,
// injects CommanderClaims into the request context, and copies the scopes from
// the JWT's "scopes" claim into the authz context.
//
// Cookie takes precedence if both are present.
// Returns 401 if no credential is present or the token is invalid/revoked.
//
// A JWT minted before the scopes-claim rollout will have an empty Scopes slice,
// in which case no scopes are injected. Downstream scope checks will fail
// closed — the commander must re-authenticate to pick up their scopes.
//
// Does NOT panic if commanderJWTValidator is nil — returns 503 with a clear error.
func (s *Server) withCommanderAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.commanderJWTValidator == nil {
			s.writeError(w, http.StatusServiceUnavailable, "commander auth not configured")
			return
		}

		// Extract token: cookie takes precedence over Authorization header.
		var token string
		if cookie, err := r.Cookie(s.cfg.CommanderAuth.CookieName); err == nil && cookie.Value != "" {
			token = cookie.Value
		} else {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if token == "" {
			s.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		claims, err := s.commanderJWTValidator.Validate(r.Context(), token)
		if err != nil {
			if errors.Is(err, auth.ErrTokenRevoked) {
				s.writeError(w, http.StatusUnauthorized, "token revoked")
				return
			}
			s.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		// Inject claims and scopes into context. Scopes come from the JWT's
		// "scopes" claim — there is no fallback, so a legacy JWT issued
		// before this rollout yields an empty scope context and all scope
		// checks fail closed.
		ctx := auth.WithCommanderClaims(r.Context(), claims)
		scopes := make([]authz.Scope, 0, len(claims.Scopes))
		for _, s := range claims.Scopes {
			scopes = append(scopes, authz.Scope(s))
		}
		ctx = authz.ContextWithScopes(ctx, scopes...)
		r = r.WithContext(ctx)

		next(w, r)
	}
}

// fidFromContext is a package-level helper for handlers that need the FID.
// Returns ErrNoFIDInContext if withCommanderAuth was not applied.
// Does NOT panic.
func fidFromContext(ctx context.Context) (string, error) {
	return auth.FIDFromContext(ctx)
}
