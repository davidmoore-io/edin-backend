package memgraph

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// maxQueryLen is the upper bound on the length of user input accepted by
// buildSearchPlan. ED system names top out at 76 chars (longest observed
// station name in production), so 128 leaves comfortable headroom for
// multi-token queries while rejecting pathological client input.
const maxQueryLen = 128

// errEmptyQuery is returned when the input is empty, whitespace-only, or
// contains no alphanumeric characters at all.
var errEmptyQuery = errors.New("text_search: query is empty or has no searchable terms")

// errQueryTooLong is returned when the input exceeds maxQueryLen.
var errQueryTooLong = errors.New("text_search: query exceeds length cap")

// tokenSplitter mirrors Tantivy's default analyzer: split on any run of
// non-alphanumeric characters. Empirically verified against Memgraph 3.8.1:
// "BD+45 1882" indexes as terms ["bd", "45", "1882"]; "Alpha Centauri" as
// ["alpha", "centauri"]. The split must match index-time behaviour or the
// query will look for tokens that don't exist.
var tokenSplitter = regexp.MustCompile(`[^a-z0-9]+`)

// SearchPlan is the parsed form of a user query.
//
// For single-token queries we fall back to regex_search, which supports the
// `term.*` prefix pattern Tantivy needs for autocomplete. For multi-token
// queries we use text_search.search with a boolean AND query across all
// completed tokens — this sidesteps the 1000-result cap that regex_search hits
// when a single anchor token (e.g. "4" in "c5-4") is far too common.
//
// The last token is always handled via a Cypher CONTAINS post-filter rather
// than a Tantivy term, because the user may be mid-word ("cent" for
// "centauri") and mid-word strings are not standalone indexed terms.
type SearchPlan struct {
	// SearchQuery is a Tantivy boolean AND query for multi-token searches,
	// e.g. "data.name:col AND data.name:359 AND data.name:sector".
	// Empty for single-token searches — use Regex instead.
	SearchQuery string

	// Regex is used for single-token searches only; passed to regex_search.
	// Form: "<token>.*". Empty for multi-token searches.
	Regex string

	// LastToken is applied as a Cypher post-filter for multi-token queries:
	//   WHERE toLower(s.name) CONTAINS lastToken
	// Handles the last (possibly partial) typed token. Empty for single-token
	// queries (covered by the Regex prefix instead).
	LastToken string
}

// buildSearchPlan parses a user query into a SearchPlan. Returns an error
// for empty, whitespace-only, over-long, or all-reserved-character inputs.
//
// The function lowercases input and splits on non-alphanumeric runs to match
// Tantivy's default tokenization. Tokens are pure alphanumerics, so no regex
// metacharacter escaping is required.
func buildSearchPlan(raw string) (SearchPlan, error) {
	if len(raw) > maxQueryLen {
		return SearchPlan{}, errQueryTooLong
	}
	lower := strings.ToLower(raw)
	tokens := tokenSplitter.Split(lower, -1)
	// Filter empty tokens that result from leading/trailing/double-separators.
	out := tokens[:0]
	for _, t := range tokens {
		if t != "" {
			out = append(out, t)
		}
	}
	tokens = out
	if len(tokens) == 0 {
		return SearchPlan{}, errEmptyQuery
	}

	// Single token: regex_search gives us free prefix matching ("sol.*" →
	// Sol, Solati, Sollaro…). No post-filter needed.
	if len(tokens) == 1 {
		return SearchPlan{Regex: tokens[0] + ".*"}, nil
	}

	// Multi-token: build a Tantivy boolean AND query from all completed tokens
	// (everything except the last), then use a Cypher CONTAINS filter for the
	// last (possibly partial) token. The AND query is far more selective than
	// a single-token regex, so the 1000-result cap is not an issue in practice.
	completed := tokens[:len(tokens)-1]
	parts := make([]string, len(completed))
	for i, t := range completed {
		parts[i] = fmt.Sprintf("data.name:%s", t)
	}

	return SearchPlan{
		SearchQuery: strings.Join(parts, " AND "),
		LastToken:   tokens[len(tokens)-1],
	}, nil
}
