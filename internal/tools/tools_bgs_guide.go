package tools

import (
	"context"
	_ "embed"
	"errors"
	"sort"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

//go:embed data/bgsguide.txt
var bgsGuideText string

// Approx 4 chars per token — a 2000-token chunk ≈ 8000 chars.
const (
	bgsChunkChars      = 8000
	bgsChunkHalfWindow = bgsChunkChars / 2
	bgsMaxChunks       = 5
	bgsDefaultChunks   = 3
	bgsMergeGap        = 3000 // merge match clusters closer than this many chars apart
)

// bgsGroundingRule is returned with every response as a hard reminder to the
// model that answers must be grounded strictly in the returned chunks.
const bgsGroundingRule = "!IMPORTANT — STRICT GROUNDING RULE: Answer using ONLY the text in the 'chunks' field above. Do NOT assume, presuppose, infer, extrapolate, or use any other knowledge about Elite Dangerous BGS, even if you believe you know the answer. If the chunks do not contain enough information, say so explicitly and offer to search with a different keyword. Quote or paraphrase the chunks directly; do not embellish."

// bgsGuideSearch performs a case-insensitive keyword search over the embedded
// BGS guide and returns text windows of roughly 2000 tokens around each match
// cluster. Intended as a lightweight, non-vectorised retrieval surface.
func (e *Executor) bgsGuideSearch(ctx context.Context, args map[string]any) (any, error) {
	// Available to both ops and Kaine chat.
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}

	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query parameter required")
	}
	if len(query) < 2 {
		return nil, errors.New("query must be at least 2 characters")
	}

	maxChunks := getInt(args, "max_chunks", bgsDefaultChunks)
	if maxChunks < 1 {
		maxChunks = 1
	}
	if maxChunks > bgsMaxChunks {
		maxChunks = bgsMaxChunks
	}

	text := bgsGuideText
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
			"note":          "no matches found in the BGS guide",
			"!IMPORTANT":    bgsGroundingRule,
		}, nil
	}

	// Cluster nearby offsets so we don't return overlapping chunks.
	type cluster struct {
		start, end, count int
	}
	clusters := []cluster{{start: offsets[0], end: offsets[0], count: 1}}
	for _, off := range offsets[1:] {
		last := &clusters[len(clusters)-1]
		if off-last.end <= bgsMergeGap {
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
		from := mid - bgsChunkHalfWindow
		if from < 0 {
			from = 0
		}
		to := from + bgsChunkChars
		if to > len(text) {
			to = len(text)
			from = to - bgsChunkChars
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
		"source":        "The Complete Elite Dangerous BGS Guide 2025 v3.0 (Cmdr Purrfect / Andrew van der Stock)",
		"!IMPORTANT":    bgsGroundingRule,
	}, nil
}
