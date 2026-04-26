package features_test

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/stretchr/testify/require"
)

// TestRegistry_NotEmpty: skipped until Phase 13 populates the registry.
// The skip is the only plan-sanctioned t.Skip in the project — see the
// edin-bot plan part 4 task 8.1 step 6. Phase 13 unskips it.
func TestRegistry_NotEmpty(t *testing.T) {
	if len(features.Registry) == 0 {
		t.Skip("Registry empty — concrete features land in Phase 12; main.go populates Registry in Phase 13")
	}
	require.NotEmpty(t, features.Registry)
}

func TestRegistry_EveryEntrySatisfiesExactlyOneSubInterface(t *testing.T) {
	for name, f := range features.Registry {
		_, isPoll := f.(features.PollFeature)
		_, isEvent := f.(features.EventDrivenFeature)

		switch {
		case isPoll && isEvent:
			t.Errorf("%s: must implement EITHER PollFeature OR EventDrivenFeature, not both", name)
		case !isPoll && !isEvent:
			t.Errorf("%s: must implement at least one of PollFeature/EventDrivenFeature", name)
		}
	}
}

func TestRegistry_NameMethodMatchesKey(t *testing.T) {
	for name, f := range features.Registry {
		require.Equal(t, name, f.Name(),
			"Registry key must equal Feature.Name() for %s", name)
	}
}

// Validate(DefaultConfig()) MUST succeed for every registered feature — a
// feature that fails its own default config validation is a definitional bug.
func TestRegistry_DefaultConfigIsValid(t *testing.T) {
	for name, f := range features.Registry {
		require.NoError(t, f.Validate(f.DefaultConfig()),
			"%s.Validate(DefaultConfig()) must not error", name)
	}
}
