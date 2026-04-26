package tools

import (
	"context"
	_ "embed"
	"errors"
	"sort"
	"strings"
)

//go:embed data/powerplayguide.txt
var powerplayGuideText string

// The Powerplay refcard is much smaller than the BGS guide (~12KB vs
// ~228KB), so a tighter chunk window keeps retrievals focused on the
// matching table rows rather than dumping most of the document on every
// call. ~1000-token chunks ≈ 4000 chars.
const (
	powerplayChunkChars      = 4000
	powerplayChunkHalfWindow = powerplayChunkChars / 2
	powerplayMaxChunks       = 5
	powerplayDefaultChunks   = 2
	powerplayMergeGap        = 1500 // merge match clusters closer than this many chars apart
)

// powerplayGroundingRule mirrors the BGS guide's rule and is returned with
// every response so the LLM is reminded that answers must be grounded
// strictly in the returned chunks.
const powerplayGroundingRule = "!IMPORTANT — STRICT GROUNDING RULE: Answer using ONLY the text in the 'chunks' field above. Do NOT assume, presuppose, infer, extrapolate, or use any other knowledge about Elite Dangerous Powerplay, even if you believe you know the answer. If the chunks do not contain enough information, say so explicitly and offer to search with a different keyword. Quote or paraphrase the chunks directly; do not embellish."

// powerplayGuideSearch performs a case-insensitive keyword search over the
// embedded Powerplay refcard and returns text windows around each match
// cluster. Mirrors bgsGuideSearch's shape so the LLM sees a consistent
// retrieval-tool interface across reference corpora.
func (e *Executor) powerplayGuideSearch(ctx context.Context, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query parameter required")
	}
	if len(query) < 2 {
		return nil, errors.New("query must be at least 2 characters")
	}

	maxChunks := getInt(args, "max_chunks", powerplayDefaultChunks)
	if maxChunks < 1 {
		maxChunks = 1
	}
	if maxChunks > powerplayMaxChunks {
		maxChunks = powerplayMaxChunks
	}

	text := powerplayGuideText
	lower := strings.ToLower(text)
	qLower := strings.ToLower(query)

	// Find every occurrence offset.
	var offsets []int
	start := 0
	for {
		idx := strings.Index(lower[start:], qLower)
		if idx < 0 {
			break
		}
		offsets = append(offsets, start+idx)
		start = start + idx + len(qLower)
	}

	if len(offsets) == 0 {
		return map[string]any{
			"query":         query,
			"total_matches": 0,
			"chunks":        []any{},
			"note":          "no matches found in the Powerplay refcard",
			"!IMPORTANT":    powerplayGroundingRule,
		}, nil
	}

	// Cluster nearby offsets so we don't return overlapping chunks.
	type cluster struct {
		start, end, count int
	}
	clusters := []cluster{{start: offsets[0], end: offsets[0], count: 1}}
	for _, off := range offsets[1:] {
		last := &clusters[len(clusters)-1]
		if off-last.end <= powerplayMergeGap {
			last.end = off
			last.count++
		} else {
			clusters = append(clusters, cluster{start: off, end: off, count: 1})
		}
	}

	// Rank clusters by match density (count), stable order on offset.
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].count != clusters[j].count {
			return clusters[i].count > clusters[j].count
		}
		return clusters[i].start < clusters[j].start
	})

	if len(clusters) > maxChunks {
		clusters = clusters[:maxChunks]
	}

	// Build window around each cluster midpoint.
	chunks := make([]map[string]any, 0, len(clusters))
	for _, c := range clusters {
		mid := (c.start + c.end) / 2
		from := mid - powerplayChunkHalfWindow
		if from < 0 {
			from = 0
		}
		to := from + powerplayChunkChars
		if to > len(text) {
			to = len(text)
			from = to - powerplayChunkChars
			if from < 0 {
				from = 0
			}
		}
		// Nudge window to word boundaries for cleaner output.
		if from > 0 {
			if i := strings.IndexByte(text[from:], '\n'); i >= 0 && i < 200 {
				from += i + 1
			}
		}
		if to < len(text) {
			if i := strings.LastIndexByte(text[:to], '\n'); i > from && (to-i) < 200 {
				to = i
			}
		}

		chunks = append(chunks, map[string]any{
			"offset":           from,
			"length":           to - from,
			"matches_in_chunk": c.count,
			"text":             text[from:to],
		})
	}

	return map[string]any{
		"query":         query,
		"total_matches": len(offsets),
		"total_chunks":  len(chunks),
		"chunks":        chunks,
		"source":        "Elite Dangerous Powerplay Reference Card (heatmap.sotl.org.uk/powers/refcard)",
		"!IMPORTANT":    powerplayGroundingRule,
	}, nil
}
