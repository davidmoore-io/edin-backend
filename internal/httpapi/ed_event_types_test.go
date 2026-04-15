package httpapi

import "testing"

func TestAllowedEventTypes_ContainsCoreEvents(t *testing.T) {
	coreEvents := []string{
		"FSDJump", "Docked", "MissionAccepted", "Scan", "MarketBuy",
		"Location", "Undocked", "FSSDiscoveryScan", "Loadout", "LoadGame",
	}
	for _, ev := range coreEvents {
		if !AllowedEDEventTypes[ev] {
			t.Errorf("expected core event %q to be allowed", ev)
		}
	}
}

func TestAllowedEventTypes_RejectsUnknown(t *testing.T) {
	unknowns := []string{"HackedEvent", "DoSomethingBad", "", "FSDJUMP_EXTRA"}
	for _, ev := range unknowns {
		if AllowedEDEventTypes[ev] {
			t.Errorf("expected unknown event %q to be rejected", ev)
		}
	}
}

func TestAllowedEventTypes_IsCaseSensitive(t *testing.T) {
	caseMismatches := []string{"fsdjump", "docked", "FSDJUMP", "fsdJump", "DOCKED"}
	for _, ev := range caseMismatches {
		if AllowedEDEventTypes[ev] {
			t.Errorf("expected case-mismatched event %q to be rejected (map is case-sensitive)", ev)
		}
	}
}

func TestAllowedEventTypes_MinimumCount150(t *testing.T) {
	count := len(AllowedEDEventTypes)
	if count < 150 {
		t.Errorf("AllowedEDEventTypes has %d entries, want at least 150", count)
	}
}
