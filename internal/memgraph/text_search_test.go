package memgraph

import (
	"reflect"
	"strings"
	"testing"
)

// These tests pin the query-planning semantics of buildSearchPlan, the function
// that turns user input into the regex + must-contain pair we feed to Memgraph's
// text_search.regex_search and the Cypher post-filter.
//
// Why regex_search rather than `term*` Tantivy prefix wildcards:
//   Memgraph 3.8.1's text_search.search does not honour term-prefix wildcards
//   (verified empirically — `data.name:cent*` returns no results even when an
//   indexed term "centauri" exists). regex_search applies a regex per indexed
//   term, which gives us the prefix behaviour we need.

func TestBuildSearchPlan_SingleToken(t *testing.T) {
	got, err := buildSearchPlan("sol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want %q", got.Regex, "sol.*")
	}
	if len(got.MustContain) != 0 {
		t.Errorf("MustContain: expected empty, got %v", got.MustContain)
	}
}

func TestBuildSearchPlan_LowercasesMixedCase(t *testing.T) {
	got, err := buildSearchPlan("Sol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want %q", got.Regex, "sol.*")
	}
}

func TestBuildSearchPlan_TrimsWhitespace(t *testing.T) {
	got, err := buildSearchPlan("  sol  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "sol.*" {
		t.Errorf("Regex: got %q want %q", got.Regex, "sol.*")
	}
	if len(got.MustContain) != 0 {
		t.Errorf("MustContain: expected empty, got %v", got.MustContain)
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
	// Tokenizer splits on non-alphanumeric runs, so "+++" → no tokens → error.
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
// indexer splits on. The user's input must be split the same way. These tests
// pin that behaviour so regressions in the tokeniser fail loudly.
func TestBuildSearchPlan_TokenisesOnNonAlphanumeric(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantRegex   string
		wantContain []string
	}{
		// "BD+45" — the user typed two adjacent indexed terms split by '+'.
		// Tokeniser yields ["bd", "45"]; last gets `.*`, earlier becomes a
		// must-contain so we don't match arbitrary BD-prefixed systems.
		{"BD+45", "BD+45", "45.*", []string{"bd"}},
		// Hyphen splits identically.
		{"V886-Centauri", "V886-Centauri", "centauri.*", []string{"v886"}},
		// Colon splits.
		{"HIP:1234", "HIP:1234", "1234.*", []string{"hip"}},
		// Period splits — 5 G. Capricorni is a real ED name.
		{"5 G. Capricorni", "5 G. Capricorni", "capricorni.*", []string{"5", "g"}},
		// Forward slash splits.
		{"col/359", "col/359", "359.*", []string{"col"}},
		// User-typed wildcards/punctuation are simply split, not literal regex.
		{"sol*", "sol*", "sol.*", nil},
		{"sol?", "sol?", "sol.*", nil},
		{"sol\\foo", "sol\\foo", "foo.*", []string{"sol"}},
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
			if !reflect.DeepEqual(got.MustContain, sliceOrNil(tc.wantContain)) {
				t.Errorf("MustContain: got %v want %v", got.MustContain, tc.wantContain)
			}
		})
	}
}

func TestBuildSearchPlan_MultiTokenWhitespace(t *testing.T) {
	// "alpha cent" — last token gets prefix, earlier becomes must-contain.
	got, err := buildSearchPlan("alpha cent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "cent.*" {
		t.Errorf("Regex: got %q want cent.*", got.Regex)
	}
	if !reflect.DeepEqual(got.MustContain, []string{"alpha"}) {
		t.Errorf("MustContain: got %v want [alpha]", got.MustContain)
	}
}

func TestBuildSearchPlan_MultiTokenWithReservedChar(t *testing.T) {
	// "BD+45 1882" → tokens [bd, 45, 1882]; regex on last, others must-contain.
	got, err := buildSearchPlan("BD+45 1882")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Regex != "1882.*" {
		t.Errorf("Regex: got %q", got.Regex)
	}
	if !reflect.DeepEqual(got.MustContain, []string{"bd", "45"}) {
		t.Errorf("MustContain: got %v want [bd 45]", got.MustContain)
	}
}

// sliceOrNil normalises an empty want-slice to nil so reflect.DeepEqual matches
// MustContain when there are no earlier tokens (we use append([]string(nil), …)
// which yields nil for the empty case).
func sliceOrNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
