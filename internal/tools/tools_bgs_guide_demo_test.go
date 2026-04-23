package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

// TestBgsGuideSearch_Demo_FactionExpansion is a demo test that dumps what the
// tool would return when the model asks about faction expansion. Run with:
//   go test -run TestBgsGuideSearch_Demo_FactionExpansion -v ./internal/tools/
func TestBgsGuideSearch_Demo_FactionExpansion(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeKaineChat)
	e := &Executor{}

	for _, query := range []string{"faction expansion", "expansion", "Expansion state"} {
		result, err := e.bgsGuideSearch(ctx, map[string]any{
			"query":      query,
			"max_chunks": 3,
		})
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", query, err)
		}

		m := result.(map[string]any)
		fmt.Printf("\n================= query: %q =================\n", m["query"])
		fmt.Printf("total_matches: %d   total_chunks: %v\n", m["total_matches"], m["total_chunks"])

		// chunks is []map[string]any when there are results, []any{} when empty.
		switch chunks := m["chunks"].(type) {
		case []map[string]any:
			for i, c := range chunks {
				fmt.Printf("\n--- CHUNK %d (offset %d, length %d, matches_in_chunk %d) ---\n",
					i+1, c["offset"], c["length"], c["matches_in_chunk"])
				fmt.Println(c["text"])
				fmt.Println("--- end chunk ---")
			}
		case []any:
			fmt.Println("(no chunks)")
		}
	}
}
