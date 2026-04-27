package memgraph

import (
	"errors"
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

// SearchPlan is the parsed form of a user query. It carries everything
// needed to run a token-prefix search via Memgraph's text_search.regex_search
// procedure plus a Cypher post-filter.
//
// Why regex_search rather than text_search.search:
//   In Memgraph 3.8.1, the standard `text_search.search` procedure does NOT
//   support `term*` prefix wildcards — `data.name:cent*` returns no results
//   even when an indexed term "centauri" exists. `regex_search` does support
//   regex-style prefix matching (regex applied per indexed term). The cost is
//   that we lose the boolean AND syntax across multiple terms, so we emulate
//   it: the LAST token becomes a regex prefix, and earlier tokens become
//   literal substrings the system name must contain.
type SearchPlan struct {
	// Regex is passed to text_search.regex_search. It is of the form
	// `<lastToken>.*` and matches any indexed term beginning with the last
	// (potentially incomplete) token typed by the user.
	Regex string

	// MustContain holds the lowercased earlier tokens. The Cypher post-filter
	// ANDs `toLower(name) CONTAINS t` for each of these — cheap because the
	// regex_search has already narrowed the candidate set to a small handful.
	// Empty for single-token queries.
	MustContain []string
}

// buildSearchPlan parses a user query into a SearchPlan. Returns an error
// for empty, whitespace-only, over-long, or all-reserved-character inputs.
//
// The function lowercases input and splits on non-alphanumeric runs to match
// Tantivy's default tokenization. Tokens are pure alphanumerics, so no regex
// metacharacter escaping is required when assembling the regex.
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

	last := tokens[len(tokens)-1]
	earlier := tokens[:len(tokens)-1]

	plan := SearchPlan{
		Regex:       last + ".*",
		MustContain: append([]string(nil), earlier...),
	}
	return plan, nil
}
