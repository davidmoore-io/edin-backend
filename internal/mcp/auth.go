package mcp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// TokenValidator validates a Bearer token and returns the user's groups.
// Implemented by httpapi.JWTValidator via an adapter.
type TokenValidator interface {
	// ValidateAndGetGroups validates a JWT and returns the user's group names.
	ValidateAndGetGroups(token string) ([]string, error)
}

// WellKnownHandler serves the OAuth Protected Resource Metadata (RFC 9728)
// at /.well-known/oauth-protected-resource so MCP clients can auto-discover
// the authorization server.
type WellKnownHandler struct {
	ResourceURL  string // e.g. "https://edin.space/mcp"
	AuthServerURL string // e.g. "https://auth.edin.space/application/o/edin-mcp/"
}

func (h *WellKnownHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metadata := map[string]any{
		"resource":                  h.ResourceURL,
		"authorization_servers":     []string{h.AuthServerURL},
		"scopes_supported":         []string{"openid", "profile", "email", "groups"},
		"bearer_methods_supported": []string{"header"},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	json.NewEncoder(w).Encode(metadata)
}

// groupsToScopes maps Authentik group names to authorization scopes.
// Same mapping as the Kaine portal HTTP auth.
func groupsToScopes(groups []string) []authz.Scope {
	var scopes []authz.Scope

	hasGroup := func(name string) bool {
		target := "kaine-" + name
		targetTest := "kaine-" + name + "-test"
		for _, g := range groups {
			if g == target || g == targetTest {
				return true
			}
		}
		return false
	}

	// God mode users get full access
	if hasGroup("god") {
		return []authz.Scope{authz.ScopeAdmin, authz.ScopeLlmOperator, authz.ScopeKaineChat}
	}

	// Approved users get Kaine chat scope
	if hasGroup("approved") || hasGroup("chat") || hasGroup("chat-debug") {
		scopes = append(scopes, authz.ScopeKaineChat)
	}

	// Ops users get LLM operator scope
	if hasGroup("directors") || hasGroup("lead-ops") {
		scopes = append(scopes, authz.ScopeLlmOperator)
	}

	return scopes
}

// extractBearerToken pulls the token from an Authorization: Bearer header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return auth[7:]
}
