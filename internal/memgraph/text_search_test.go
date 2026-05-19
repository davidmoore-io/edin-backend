package memgraph

import (
	"strings"
	"testing"
)

// These tests pin the query-planning semantics of buildSearchPlan, which turns
// user input into either a regex_search call (single token) or a
// text_search.search boolean AND call (multi-token) plus a Cypher CONTAINS
// post-filter for the last (possibly partial) token.

func TestBuildSearchPlan_SingleToken(t *testing.T) {
	got, err := buildSearchPlan("sol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want sol.*", got.Regex)
	}
	if got.SearchQuery != "" {
		t.Errorf("SearchQuery: expected empty for single token, got %q", got.SearchQuery)
	}
	if got.LastToken != "" {
		t.Errorf("LastToken: expected empty for single token, got %q", got.LastToken)
	}
}

func TestBuildSearchPlan_LowercasesMixedCase(t *testing.T) {
	got, err := buildSearchPlan("Sol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want sol.*", got.Regex)
	}
}

func TestBuildSearchPlan_TrimsWhitespace(t *testing.T) {
	got, err := buildSearchPlan("  sol  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want sol.*", got.Regex)
	}
}

func TestBuildSearchPlan_RejectsEmpty(t *testing.T) {
	if _, err := buildSearchPlan(""); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestBuildSearchPlan_RejectsAllWhitespace(t *testing.T) {
	if _, err := buildSearchPlan("   \t\n"); err == nil {
		t.Fatal("expected error for whitespace-only input")
	}
}

func TestBuildSearchPlan_RejectsAllReservedChars(t *testing.T) {
	if _, err := buildSearchPlan("+++"); err == nil {
		t.Fatal("expected error for input with no alphanumeric chars")
	}
}

func TestBuildSearchPlan_LengthCap(t *testing.T) {
	long := strings.Repeat("a", 200)
	if _, err := buildSearchPlan(long); err == nil {
		t.Fatal("expected error for over-long input")
	}
}

// Procgen / catalogue names contain non-alphanumeric chars that the Tantivy
// indexer splits on. These tests pin the tokenisation so regressions fail loud.
func TestBuildSearchPlan_TokenisesOnNonAlphanumeric(t *testing.T) {
	cases := []struct {
		name            string
		input           string
		wantRegex       string // non-empty → single-token path
		wantSearchQuery string // non-empty → multi-token path
		wantLastToken   string
	}{
		// Single-token inputs — only Regex is set.
		{"sol*", "sol*", "sol.*", "", ""},
		{"sol?", "sol?", "sol.*", "", ""},

		// Two-token inputs: AND query on first token, CONTAINS on last.
		{"BD+45", "BD+45", "", "data.name:bd", "45"},
		{"V886-Centauri", "V886-Centauri", "", "data.name:v886", "centauri"},
		{"HIP:1234", "HIP:1234", "", "data.name:hip", "1234"},
		{"col/359", "col/359", "", "data.name:col", "359"},
		{"sol\\foo", "sol\\foo", "", "data.name:sol", "foo"},
		{"alpha cent", "alpha cent", "", "data.name:alpha", "cent"},

		// Three-token inputs.
		{"BD+45 1882", "BD+45 1882", "", "data.name:bd AND data.name:45", "1882"},
		{"5 G. Capricorni", "5 G. Capricorni", "", "data.name:5 AND data.name:g", "capricorni"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSearchPlan(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Regex != tc.wantRegex {
				t.Errorf("Regex: got %q want %q", got.Regex, tc.wantRegex)
			}
			if got.SearchQuery != tc.wantSearchQuery {
				t.Errorf("SearchQuery: got %q want %q", got.SearchQuery, tc.wantSearchQuery)
			}
			if got.LastToken != tc.wantLastToken {
				t.Errorf("LastToken: got %q want %q", got.LastToken, tc.wantLastToken)
			}
		})
	}
}

// Regression: "Col 359 Sector RX-R c5-4" previously anchored on "4.*" via
// regex_search, hitting the 1000-result cap. The AND query on all completed
// tokens shrinks the candidate set to a handful before the CONTAINS check.
func TestBuildSearchPlan_Col359RegressionCase(t *testing.T) {
	got, err := buildSearchPlan("Col 359 Sector RX-R c5-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "" {
		t.Errorf("Regex: expected empty for multi-token, got %q", got.Regex)
	}
	wantQuery := "data.name:col AND data.name:359 AND data.name:sector AND data.name:rx AND data.name:r AND data.name:c5"
	if got.SearchQuery != wantQuery {
		t.Errorf("SearchQuery:\n  got  %q\n  want %q", got.SearchQuery, wantQuery)
	}
	if got.LastToken != "4" {
		t.Errorf("LastToken: got %q want %q", got.LastToken, "4")
	}
}
