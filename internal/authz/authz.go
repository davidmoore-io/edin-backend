package authz

// Scope represents a named permission.
type Scope string

const (
	ScopeAdmin       Scope = "admin"
	ScopeLlmOperator Scope = "llm_operator" // Full ops access (Discord operators)
	ScopeKaineChat   Scope = "kaine_chat"   // Limited Elite queries (public users)
)

// Resolver determines scopes available to a principal.
type Resolver interface {
	ResolveScopes(userRoles []string) []Scope
}

// Allow checks whether the target scope is present in the provided slice.
func Allow(scopes []Scope, target Scope) bool {
	for _, s := range scopes {
		if s == target {
			return true
		}
	}
	return false
}

// RoleResolver resolves scopes based on Discord role IDs.
type RoleResolver struct {
	adminRoles map[string]struct{}
	llmRoles   map[string]struct{}
}

// NewRoleResolver constructs a resolver using allowed role IDs.
func NewRoleResolver(adminRoleIDs, llmRoleIDs []string) *RoleResolver {
	return &RoleResolver{
		adminRoles: toSet(adminRoleIDs),
		llmRoles:   toSet(llmRoleIDs),
	}
}

// ResolveScopes maps Discord role IDs to scopes.
func (r *RoleResolver) ResolveScopes(userRoles []string) []Scope {
	if r == nil {
		return nil
	}
	var scopes []Scope
	for _, role := range userRoles {
		if _, ok := r.adminRoles[role]; ok && !containsScope(scopes, ScopeAdmin) {
			scopes = append(scopes, ScopeAdmin)
		}
		if _, ok := r.llmRoles[role]; ok && !containsScope(scopes, ScopeLlmOperator) {
			scopes = append(scopes, ScopeLlmOperator)
		}
	}
	return scopes
}

// StaticResolver always returns the provided scopes.
type StaticResolver struct {
	scopes []Scope
}

// NewStaticResolver constructs a static resolver.
func NewStaticResolver(scopes ...Scope) *StaticResolver {
	return &StaticResolver{scopes: scopes}
}

// ResolveScopes implements Resolver.
func (r *StaticResolver) ResolveScopes(_ []string) []Scope {
	return append([]Scope(nil), r.scopes...)
}

func toSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		set[item] = struct{}{}
	}
	return set
}

func containsScope(scopes []Scope, scope Scope) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}
