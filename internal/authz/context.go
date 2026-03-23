package authz

import "context"

type contextKey string

const scopesContextKey contextKey = "authz/scopes"

// ContextWithScopes returns a copy of the context that includes the provided scopes.
// Passing duplicate or empty scopes is ignored to keep the stored slice minimal.
func ContextWithScopes(ctx context.Context, scopes ...Scope) context.Context {
	if len(scopes) == 0 {
		return ctx
	}
	existing := ScopesFromContext(ctx)
	if len(existing) == 0 && len(scopes) == 1 {
		return context.WithValue(ctx, scopesContextKey, []Scope{scopes[0]})
	}

	merged := make([]Scope, 0, len(existing)+len(scopes))
	merged = append(merged, existing...)
	for _, scope := range ScopeList(scopes...) {
		if scope == "" || Allow(merged, scope) {
			continue
		}
		merged = append(merged, scope)
	}
	return context.WithValue(ctx, scopesContextKey, merged)
}

// ScopesFromContext extracts any scopes that have been associated with the context.
// The returned slice is a copy to prevent accidental modification.
func ScopesFromContext(ctx context.Context) []Scope {
	if ctx == nil {
		return nil
	}
	if scopes, ok := ctx.Value(scopesContextKey).([]Scope); ok && len(scopes) > 0 {
		out := make([]Scope, len(scopes))
		copy(out, scopes)
		return out
	}
	return nil
}

// ScopeList normalises a set of scopes by filtering empty entries.
func ScopeList(scopes ...Scope) []Scope {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]Scope, 0, len(scopes))
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		out = append(out, scope)
	}
	return out
}
