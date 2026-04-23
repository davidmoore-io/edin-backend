package authz

import "sort"

// groupScopes maps Authentik group names to the set of scopes they grant.
//
// The mapping encodes EDIN's two-tier access model:
//
//   - Kaine groups (kaine-*) grant kaine_chat plus read access to
//     galaxy data. kaine-approved adds mining tools. kaine-god is the
//     superset (admin + llm_operator + everything).
//
//   - edin-copilot grants copilot_chat + galaxy read + the commander's
//     own data. edin-copilot-trusted layers on kaine_mining so trusted
//     commanders can see mining intel alongside their own journal.
//
// Both kaine-chat and kaine-chat-test map to the same set so that
// test accounts exercise production code paths end-to-end.
var groupScopes = map[string][]Scope{
	"kaine-god": {
		ScopeAdmin,
		ScopeLlmOperator,
		ScopeKaineChat,
		ScopeGalaxyRead,
		ScopeKaineMining,
		ScopeCommanderData,
	},
	"kaine-approved": {
		ScopeKaineChat,
		ScopeGalaxyRead,
		ScopeKaineMining,
	},
	"kaine-chat": {
		ScopeKaineChat,
		ScopeGalaxyRead,
	},
	"kaine-chat-test": {
		ScopeKaineChat,
		ScopeGalaxyRead,
	},
	"kaine-chat-debug": {
		ScopeKaineChat,
		ScopeGalaxyRead,
	},
	"edin-copilot": {
		ScopeCopilotChat,
		ScopeGalaxyRead,
		ScopeCommanderData,
	},
	"edin-copilot-trusted": {
		ScopeCopilotChat,
		ScopeGalaxyRead,
		ScopeCommanderData,
		ScopeKaineMining,
	},
}

// ScopesForGroups maps a list of Authentik group names to the union of
// scopes they grant. Unknown groups are ignored silently so adding a
// new group in Authentik that the backend does not yet recognise is a
// no-op rather than an error.
//
// The returned slice is deduplicated and sorted lexicographically for
// deterministic output. A nil or empty input returns a nil slice.
func ScopesForGroups(groups []string) []Scope {
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[Scope]struct{})
	for _, group := range groups {
		scopes, ok := groupScopes[group]
		if !ok {
			continue
		}
		for _, scope := range scopes {
			seen[scope] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]Scope, 0, len(seen))
	for scope := range seen {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
