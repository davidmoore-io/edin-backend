package memgraph

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGetSystemWatchSnapshot_Found exercises the happy path against a real
// Memgraph instance (skips if MEMGRAPH_TEST_HOST is unset, matching the
// existing test pattern in this package). Asserts shape only — exact
// state values are volatile (BGS ticks, powerplay cycles), so the test
// pins the *contract* (slug echoed back, name set, factions
// deterministically ordered) without coupling to mutable values.
//
// Slug "Sol" is hard-coded because Sol is the most stable system in any
// production ED dataset — it always exists, never has its name changed,
// and the slug is identical to the name (no spaces).
func TestGetSystemWatchSnapshot_Found(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snap, err := client.GetSystemWatchSnapshot(ctx, "Sol")
	if err != nil {
		t.Fatalf("GetSystemWatchSnapshot(Sol): %v", err)
	}
	if snap == nil {
		t.Fatal("snapshot is nil")
	}
	if snap.Slug != "Sol" {
		t.Errorf("Slug = %q, want %q", snap.Slug, "Sol")
	}
	if snap.Name != "Sol" {
		t.Errorf("Name = %q, want %q", snap.Name, "Sol")
	}

	// Faction ordering is deterministic (influence DESC, name ASC). If Sol
	// has factions, they must appear in non-increasing influence order;
	// equal-influence pairs must appear in alphabetical order.
	for i := 1; i < len(snap.Factions); i++ {
		prev, cur := snap.Factions[i-1], snap.Factions[i]
		if cur.Influence > prev.Influence {
			t.Errorf("factions out of order at index %d: %s (%.4f) after %s (%.4f)",
				i, cur.Name, cur.Influence, prev.Name, prev.Influence)
		}
		if cur.Influence == prev.Influence && cur.Name < prev.Name {
			t.Errorf("equal-influence factions out of alpha order: %s before %s",
				cur.Name, prev.Name)
		}
	}
}

// TestGetSystemWatchSnapshot_NotFound asserts the sentinel for an unknown
// slug. The test uses a slug that's structurally valid (no spaces, contains
// dashes) but extremely unlikely to exist — protects against accidental
// matches as the prod dataset evolves.
func TestGetSystemWatchSnapshot_NotFound(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.GetSystemWatchSnapshot(ctx, "DefinitelyNotAStarSystem-zz999")
	if !errors.Is(err, ErrSystemNotFound) {
		t.Fatalf("expected ErrSystemNotFound, got %v", err)
	}
}
