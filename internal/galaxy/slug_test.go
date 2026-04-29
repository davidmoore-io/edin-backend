package galaxy_test

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/galaxy"
)

// TestSlugify locks in the slug-derivation rule against a set of real ED
// system names sampled from prod Memgraph. If this test ever fails because
// of a code change, the contract has shifted — every persisted slug in the
// graph and every URL the bot has emitted assumes the rule below. Update
// with care: a contract change requires a full backfill.
func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Real ED system names from production.
		{"plain capitalised name", "Sol", "Sol"},
		{"HIP cluster", "HIP 61332", "HIP61332"},
		{"sector with dashes", "Col 359 Sector LL-J b11-3", "Col359SectorLL-Jb11-3"},
		{"sector with deeper coordinates", "Antliae Sector VJ-R b4-4", "AntliaeSectorVJ-Rb4-4"},
		{"single token", "Shinrarta", "Shinrarta"},
		{"two tokens", "Shinrarta Dezhra", "ShinrartaDezhra"},

		// Whitespace edge cases the operator might paste.
		{"leading whitespace", "  HIP 61332", "HIP61332"},
		{"trailing whitespace", "HIP 61332  ", "HIP61332"},
		{"both ends", "  HIP 61332  ", "HIP61332"},
		{"internal double spaces", "HIP  61332", "HIP61332"},

		// Degenerate inputs — make sure nothing panics.
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := galaxy.Slugify(tc.in)
			if got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSlugify_PreservesDashes is a focused regression: the contract expressly
// keeps dashes intact, because many real ED system names embed them. If a
// future "URL-safe" tightening removed dashes too, the slug→system lookup
// would silently break for thousands of systems.
func TestSlugify_PreservesDashes(t *testing.T) {
	got := galaxy.Slugify("Col 359 Sector LL-J b11-3")
	if got != "Col359SectorLL-Jb11-3" {
		t.Fatalf("dashes must be preserved verbatim: got %q", got)
	}
}
