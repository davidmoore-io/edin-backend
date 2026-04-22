package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustParseTime is a tiny test helper so the toEventSummary assertions read
// cleanly. Panics on a parse error — callers pass constant strings.
func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTryCompactEventData_UnknownType_NoCompactorApplied(t *testing.T) {
	// Unknown event types must be left to the byte-cap fallback. Don't want
	// a future registered compactor to silently rewrite an unrelated event.
	_, ok := tryCompactEventData("SomethingNovel", json.RawMessage(`{"x":1}`))
	assert.False(t, ok)
}

func TestCompactCargo_KeepsNamesAndCountsDropsLocalisationCruft(t *testing.T) {
	raw := json.RawMessage(`{
		"event": "Cargo",
		"Vessel": "Ship",
		"Count": 1218,
		"Inventory": [
			{"Name":"$steel_name;","Name_Localised":"Steel","Count":1218,"Stolen":0},
			{"Name":"$tritium_name;","Name_Localised":"Tritium","Count":0,"Stolen":12,"MissionID":42}
		]
	}`)
	out, ok := tryCompactEventData("Cargo", raw)
	require.True(t, ok)

	var got struct {
		Vessel string `json:"vessel"`
		Total  int    `json:"total"`
		Items  []struct {
			Name      string `json:"name"`
			Count     int    `json:"count"`
			Stolen    int    `json:"stolen"`
			MissionID int64  `json:"mission_id"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "Ship", got.Vessel)
	assert.Equal(t, 1218, got.Total)
	require.Len(t, got.Items, 2)
	assert.Equal(t, "Steel", got.Items[0].Name)
	assert.Equal(t, 1218, got.Items[0].Count)
	assert.Equal(t, 0, got.Items[0].Stolen)
	assert.Equal(t, "Tritium", got.Items[1].Name)
	assert.Equal(t, 12, got.Items[1].Stolen)
	assert.Equal(t, int64(42), got.Items[1].MissionID)
}

func TestCompactCargo_FallsBackToRawNameWhenNoLocalised(t *testing.T) {
	raw := json.RawMessage(`{"event":"Cargo","Count":5,"Inventory":[{"Name":"$gold_name;","Count":5}]}`)
	out, ok := tryCompactEventData("Cargo", raw)
	require.True(t, ok)

	var got struct {
		Items []struct{ Name string `json:"name"` } `json:"items"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, "gold", got.Items[0].Name,
		"strip the $..._name; markup when no localised form is present")
}

func TestCompactConstructionDepot_SurfacesProgressAndRemaining(t *testing.T) {
	raw := json.RawMessage(`{
		"event": "ColonisationConstructionDepot",
		"MarketID": 3959710210,
		"ConstructionProgress": 0.34,
		"ConstructionComplete": false,
		"ConstructionFailed": false,
		"ResourcesRequired": [
			{"Name":"$steel_name;","Name_Localised":"Steel","RequiredAmount":5000,"ProvidedAmount":1297,"Payment":0},
			{"Name":"$cmm_composite_name;","Name_Localised":"CMM Composite","RequiredAmount":100,"ProvidedAmount":100,"Payment":0}
		]
	}`)
	out, ok := tryCompactEventData("ColonisationConstructionDepot", raw)
	require.True(t, ok)

	var got struct {
		MarketID  int64   `json:"market_id"`
		Progress  float64 `json:"progress"`
		Complete  bool    `json:"complete"`
		Resources []struct {
			Name      string `json:"name"`
			Required  int    `json:"required"`
			Provided  int    `json:"provided"`
			Remaining int    `json:"remaining"`
		} `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, int64(3959710210), got.MarketID)
	assert.Equal(t, 0.34, got.Progress)
	assert.False(t, got.Complete)
	require.Len(t, got.Resources, 2)
	assert.Equal(t, "Steel", got.Resources[0].Name)
	assert.Equal(t, 3703, got.Resources[0].Remaining)
	assert.Equal(t, 0, got.Resources[1].Remaining, "provided >= required should clamp remaining to 0")
}

func TestCompactLoadout_KeepsShipIdentityDropsModulesArray(t *testing.T) {
	// Build a Loadout with a large Modules array — the compactor should
	// keep the top-level fields and report a count rather than pass the
	// array through.
	mods := make([]map[string]any, 0, 40)
	for i := 0; i < 40; i++ {
		mods = append(mods, map[string]any{
			"Slot":     "Slot01",
			"Item":     "$int_somethingexpensive_name;",
			"On":       true,
			"Priority": 0,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"event":         "Loadout",
		"Ship":          "cutter",
		"ShipID":        12,
		"ShipName":      "Midnight Haul",
		"ShipIdent":     "CMR-01",
		"HullValue":     208000000,
		"ModulesValue":  450000000,
		"HullHealth":    1.0,
		"UnladenMass":   1100.0,
		"CargoCapacity": 704,
		"MaxJumpRange":  24.5,
		"FuelCapacity":  map[string]float64{"Main": 32, "Reserve": 0.83},
		"Rebuy":         32900000,
		"Modules":       mods,
	})
	require.NoError(t, err)

	out, ok := tryCompactEventData("Loadout", payload)
	require.True(t, ok)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "cutter", got["ship"])
	assert.Equal(t, "Midnight Haul", got["ship_name"])
	assert.EqualValues(t, 704, got["cargo_capacity"])
	assert.EqualValues(t, 40, got["modules_count"])
	_, hasModules := got["modules"]
	assert.False(t, hasModules, "raw Modules array must not be forwarded")
}

func TestCompactStoredModules_CountsByStarSystem(t *testing.T) {
	raw := json.RawMessage(`{
		"event": "StoredModules",
		"StationName": "Jameson Memorial",
		"StarSystem": "Shinrarta Dezhra",
		"Items": [
			{"StorageSlot":1,"Name":"$int_fueltank_size6_class3_name;","StarSystem":"Shinrarta Dezhra"},
			{"StorageSlot":2,"Name":"$int_powerplant_size8_class5_name;","StarSystem":"Shinrarta Dezhra"},
			{"StorageSlot":3,"Name":"$int_engine_size7_class5_name;","StarSystem":"Sol"}
		]
	}`)
	out, ok := tryCompactEventData("StoredModules", raw)
	require.True(t, ok)

	var got struct {
		Total    int `json:"total_modules"`
		BySystem []struct {
			System string `json:"system"`
			Count  int    `json:"count"`
		} `json:"by_system"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, 3, got.Total)
	systemCounts := map[string]int{}
	for _, b := range got.BySystem {
		systemCounts[b.System] = b.Count
	}
	assert.Equal(t, 2, systemCounts["Shinrarta Dezhra"])
	assert.Equal(t, 1, systemCounts["Sol"])
}

// TestCompactor_RejectsInvalidJSON — compactors receive json.RawMessage but a
// defensive unmarshal check guards against future callers passing garbage.
func TestCompactor_RejectsInvalidJSON(t *testing.T) {
	_, ok := compactCargo(json.RawMessage(`this is not json`))
	assert.False(t, ok)
	_, ok = compactConstructionDepot(json.RawMessage(`also not json`))
	assert.False(t, ok)
	_, ok = compactLoadout(json.RawMessage(`{`))
	assert.False(t, ok)
}

// TestToEventSummary_CompactsOversizedCargo exercises the integration: a
// Cargo event that overflows the byte cap in its raw journal shape but
// whose compacted form fits must come through compacted (items intact,
// total preserved) and with a "compacted" note — not omitted.
//
// Item count tuned so raw > cap > compacted: each raw inventory row is
// ~85 bytes (includes $..._name; / Name_Localised duplication), compacted
// rows are ~32 bytes each.
func TestToEventSummary_CompactsOversizedCargo(t *testing.T) {
	const itemCount = 30
	items := make([]map[string]any, 0, itemCount)
	for i := 0; i < itemCount; i++ {
		items = append(items, map[string]any{
			"Name":           "$commodity_name;",
			"Name_Localised": "Commodity",
			"Count":          100,
			"Stolen":         0,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"event":     "Cargo",
		"Vessel":    "Ship",
		"Count":     20000,
		"Inventory": items,
	})
	require.NoError(t, err)
	require.Greater(t, len(raw), perEventDataBytesCap,
		"test precondition: raw payload must exceed the cap")

	s := toEventSummary(mustParseTime("2026-04-22T23:14:00Z"), "Cargo", raw)

	assert.NotEmpty(t, s.EventData, "compactor should have produced a smaller representation")
	assert.Contains(t, s.Note, "compacted")
	assert.NotContains(t, s.Note, "omitted")
	assert.LessOrEqual(t, len(s.EventData), perEventDataBytesCap,
		"compacted payload should fit the per-event cap")

	// Compaction must preserve the total count — that's the field the model
	// will almost always want from a Cargo event.
	var parsed struct {
		Total int `json:"total"`
		Items []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(s.EventData, &parsed))
	assert.Equal(t, 20000, parsed.Total)
	assert.Len(t, parsed.Items, itemCount)
}

// TestToEventSummary_OmitsWhenCompactorItselfOvershoots — the safety net for
// pathological payloads. Construct a compacted shape that's still too big
// and assert we fall back cleanly.
func TestToEventSummary_OmitsWhenCompactorItselfOvershoots(t *testing.T) {
	// 200 construction-depot resources with long names. Even the compacted
	// shape (`{name, required, provided, remaining, payment}` ~ 70 bytes
	// each) is ~14KB — safely over the cap.
	resources := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		resources = append(resources, map[string]any{
			"Name":           "$very_long_commodity_identifier_name;",
			"Name_Localised": strings.Repeat("LongName", 4),
			"RequiredAmount": 10000,
			"ProvidedAmount": 1234,
			"Payment":        0,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"event":                "ColonisationConstructionDepot",
		"MarketID":             1,
		"ConstructionProgress": 0.1,
		"ResourcesRequired":    resources,
	})
	require.NoError(t, err)

	s := toEventSummary(mustParseTime("2026-04-22T23:14:00Z"), "ColonisationConstructionDepot", raw)
	assert.Empty(t, s.EventData, "compacted payload still over cap → omit")
	assert.Contains(t, s.Note, "omitted")
}
