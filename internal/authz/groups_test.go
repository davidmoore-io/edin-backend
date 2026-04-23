package authz

import (
	"reflect"
	"testing"
)

func TestScopesForGroups_EmptyInput_ReturnsEmpty(t *testing.T) {
	if got := ScopesForGroups(nil); got != nil {
		t.Errorf("ScopesForGroups(nil) = %v, want nil", got)
	}
	if got := ScopesForGroups([]string{}); got != nil {
		t.Errorf("ScopesForGroups([]) = %v, want nil", got)
	}
}

func TestScopesForGroups_KaineGod_GrantsFullSet(t *testing.T) {
	got := ScopesForGroups([]string{"kaine-god"})
	want := []Scope{
		ScopeAdmin,
		ScopeCommanderData,
		ScopeGalaxyRead,
		ScopeKaineChat,
		ScopeKaineMining,
		ScopeLlmOperator,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesForGroups([kaine-god]) = %v, want %v", got, want)
	}
}

func TestScopesForGroups_EdinCopilot_GrantsBaseCommanderSet(t *testing.T) {
	got := ScopesForGroups([]string{"edin-copilot"})
	want := []Scope{
		ScopeCommanderData,
		ScopeCopilotChat,
		ScopeGalaxyRead,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesForGroups([edin-copilot]) = %v, want %v", got, want)
	}
}

func TestScopesForGroups_EdinCopilotTrusted_IncludesMining(t *testing.T) {
	got := ScopesForGroups([]string{"edin-copilot-trusted"})
	want := []Scope{
		ScopeCommanderData,
		ScopeCopilotChat,
		ScopeGalaxyRead,
		ScopeKaineMining,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesForGroups([edin-copilot-trusted]) = %v, want %v", got, want)
	}
	// Sanity: trusted must be a strict superset of untrusted.
	base := ScopesForGroups([]string{"edin-copilot"})
	for _, scope := range base {
		if !Allow(got, scope) {
			t.Errorf("edin-copilot-trusted missing scope %q present in edin-copilot", scope)
		}
	}
}

func TestScopesForGroups_UnknownGroup_Ignored(t *testing.T) {
	if got := ScopesForGroups([]string{"not-a-real-group"}); got != nil {
		t.Errorf("ScopesForGroups([unknown]) = %v, want nil", got)
	}
	// Mixed with a known group, the unknown entry is silently dropped.
	got := ScopesForGroups([]string{"not-a-real-group", "kaine-chat"})
	want := []Scope{ScopeGalaxyRead, ScopeKaineChat}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesForGroups([unknown, kaine-chat]) = %v, want %v", got, want)
	}
}

func TestScopesForGroups_MultipleGroups_ScopesDeduped(t *testing.T) {
	// kaine-approved and edin-copilot both grant galaxy_read; the
	// result must include it exactly once.
	got := ScopesForGroups([]string{"kaine-approved", "edin-copilot"})
	want := []Scope{
		ScopeCommanderData,
		ScopeCopilotChat,
		ScopeGalaxyRead,
		ScopeKaineChat,
		ScopeKaineMining,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScopesForGroups([kaine-approved, edin-copilot]) = %v, want %v", got, want)
	}
	// Count each scope to confirm deduplication.
	counts := make(map[Scope]int)
	for _, scope := range got {
		counts[scope]++
	}
	for scope, count := range counts {
		if count != 1 {
			t.Errorf("scope %q appears %d times, want 1", scope, count)
		}
	}
}

func TestScopesForGroups_TestGroups_TreatedAsProd(t *testing.T) {
	prod := ScopesForGroups([]string{"kaine-chat"})
	test := ScopesForGroups([]string{"kaine-chat-test"})
	debug := ScopesForGroups([]string{"kaine-chat-debug"})
	if !reflect.DeepEqual(prod, test) {
		t.Errorf("kaine-chat (%v) and kaine-chat-test (%v) must map to the same scopes", prod, test)
	}
	if !reflect.DeepEqual(prod, debug) {
		t.Errorf("kaine-chat (%v) and kaine-chat-debug (%v) must map to the same scopes", prod, debug)
	}
}
