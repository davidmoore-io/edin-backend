package tools

import (
	"encoding/json"
	"strings"
)

// Event-data compactors — strip high-volume journal events down to the
// information that's actually useful for answering commander questions,
// preserving headroom for the model's context window.
//
// The raw Elite Dangerous journal carries a lot of bulk that's rarely
// relevant to a chat question. For example a Loadout event lists every
// module and its engineering blueprint; a Cargo event wraps each commodity
// with a localisation struct and mission metadata; a ColonisationConstruction
// Depot event enumerates every commodity requirement. For targeted questions
// like "what's in my cargo?" the LLM only needs name + count per entry, not
// the localisation blob.
//
// We only compact when the raw event is over the per-event byte cap. When
// it's already small, pass through verbatim so the model sees the exact
// journal shape it was trained on. When compaction itself overshoots (rare:
// truly enormous StoredModules blobs on carrier owners), the caller falls
// back to the "omitted" placeholder.

type eventCompactor func(json.RawMessage) (json.RawMessage, bool)

// eventCompactors maps event_type → a compactor. Registering an event here
// is an explicit decision that the model rarely needs the raw event shape
// for questions about this type.
var eventCompactors = map[string]eventCompactor{
	"Cargo":                         compactCargo,
	"ColonisationConstructionDepot": compactConstructionDepot,
	"Loadout":                       compactLoadout,
	"StoredModules":                 compactStoredModules,
	"StoredShips":                   compactStoredShips,
}

// tryCompactEventData returns a possibly-smaller rendering of event_data
// tailored to the given event_type. The bool indicates whether a compactor
// was applied (rather than whether the result fits any particular budget —
// the caller decides).
func tryCompactEventData(eventType string, raw json.RawMessage) (json.RawMessage, bool) {
	fn, ok := eventCompactors[eventType]
	if !ok {
		return nil, false
	}
	return fn(raw)
}

// ─── Cargo ────────────────────────────────────────────────────────────────────

// compactCargo → {vessel, total, items:[{name, count, stolen?, mission_id?}]}
// Drops the "$name;"/Name_Localised duplication and keeps zero-valued
// stolen/mission fields out of the wire format.
func compactCargo(raw json.RawMessage) (json.RawMessage, bool) {
	var parsed struct {
		Vessel    string `json:"Vessel"`
		Count     int    `json:"Count"`
		Inventory []struct {
			Name          string `json:"Name"`
			NameLocalised string `json:"Name_Localised"`
			Count         int    `json:"Count"`
			Stolen        int    `json:"Stolen"`
			MissionID     int64  `json:"MissionID"`
		} `json:"Inventory"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}

	type item struct {
		Name      string `json:"name"`
		Count     int    `json:"count"`
		Stolen    int    `json:"stolen,omitempty"`
		MissionID int64  `json:"mission_id,omitempty"`
	}
	items := make([]item, 0, len(parsed.Inventory))
	for _, src := range parsed.Inventory {
		items = append(items, item{
			Name:      friendlyName(src.NameLocalised, src.Name),
			Count:     src.Count,
			Stolen:    src.Stolen,
			MissionID: src.MissionID,
		})
	}

	out := map[string]any{
		"total": parsed.Count,
		"items": items,
	}
	if parsed.Vessel != "" {
		out["vessel"] = parsed.Vessel
	}
	return marshalOrNil(out)
}

// ─── ColonisationConstructionDepot ────────────────────────────────────────────

// compactConstructionDepot → {market_id, progress, complete, failed,
// resources:[{name, required, provided, remaining, payment?}]}.
// Progress is the single most-useful number; the resources array is the
// second; per-commodity payment is nice-to-have but most of the time is 0.
func compactConstructionDepot(raw json.RawMessage) (json.RawMessage, bool) {
	var parsed struct {
		MarketID             int64   `json:"MarketID"`
		ConstructionProgress float64 `json:"ConstructionProgress"`
		ConstructionComplete bool    `json:"ConstructionComplete"`
		ConstructionFailed   bool    `json:"ConstructionFailed"`
		ResourcesRequired    []struct {
			Name           string `json:"Name"`
			NameLocalised  string `json:"Name_Localised"`
			RequiredAmount int    `json:"RequiredAmount"`
			ProvidedAmount int    `json:"ProvidedAmount"`
			Payment        int64  `json:"Payment"`
		} `json:"ResourcesRequired"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}

	type res struct {
		Name      string `json:"name"`
		Required  int    `json:"required"`
		Provided  int    `json:"provided"`
		Remaining int    `json:"remaining"`
		Payment   int64  `json:"payment,omitempty"`
	}
	resources := make([]res, 0, len(parsed.ResourcesRequired))
	for _, src := range parsed.ResourcesRequired {
		remaining := src.RequiredAmount - src.ProvidedAmount
		if remaining < 0 {
			remaining = 0
		}
		resources = append(resources, res{
			Name:      friendlyName(src.NameLocalised, src.Name),
			Required:  src.RequiredAmount,
			Provided:  src.ProvidedAmount,
			Remaining: remaining,
			Payment:   src.Payment,
		})
	}

	out := map[string]any{
		"market_id": parsed.MarketID,
		"progress":  parsed.ConstructionProgress,
		"complete":  parsed.ConstructionComplete,
		"failed":    parsed.ConstructionFailed,
		"resources": resources,
	}
	return marshalOrNil(out)
}

// ─── Loadout ──────────────────────────────────────────────────────────────────

// compactLoadout keeps the ship-identity fields and the capacity numbers;
// drops the Modules array (that's the bulk). For "what ship am I flying?"
// this is all the model needs; for "detail my engineering" the model can
// be told the payload was elided.
func compactLoadout(raw json.RawMessage) (json.RawMessage, bool) {
	var parsed struct {
		Ship          string  `json:"Ship"`
		ShipID        int64   `json:"ShipID"`
		ShipName      string  `json:"ShipName"`
		ShipIdent     string  `json:"ShipIdent"`
		HullValue     int64   `json:"HullValue"`
		ModulesValue  int64   `json:"ModulesValue"`
		HullHealth    float64 `json:"HullHealth"`
		UnladenMass   float64 `json:"UnladenMass"`
		CargoCapacity int     `json:"CargoCapacity"`
		MaxJumpRange  float64 `json:"MaxJumpRange"`
		FuelCapacity  struct {
			Main    float64 `json:"Main"`
			Reserve float64 `json:"Reserve"`
		} `json:"FuelCapacity"`
		Rebuy   int64 `json:"Rebuy"`
		Modules []any `json:"Modules"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}

	out := map[string]any{
		"ship":           parsed.Ship,
		"ship_name":      parsed.ShipName,
		"ship_ident":     parsed.ShipIdent,
		"hull_value":     parsed.HullValue,
		"modules_value":  parsed.ModulesValue,
		"hull_health":    parsed.HullHealth,
		"unladen_mass":   parsed.UnladenMass,
		"cargo_capacity": parsed.CargoCapacity,
		"fuel_capacity":  parsed.FuelCapacity,
		"max_jump_range": parsed.MaxJumpRange,
		"rebuy":          parsed.Rebuy,
		"modules_count":  len(parsed.Modules),
		// Modules array deliberately not forwarded — it's the bulk of a
		// Loadout event and not needed for the questions this compactor
		// covers.
	}
	return marshalOrNil(out)
}

// ─── StoredModules ────────────────────────────────────────────────────────────

// compactStoredModules → {market_id, station, system, count, by_station:
// [{station, count}]}. Carrier owners can accumulate hundreds of entries
// here; the model almost always just wants a count or "where are my modules
// stored?".
func compactStoredModules(raw json.RawMessage) (json.RawMessage, bool) {
	var parsed struct {
		MarketID      int64  `json:"MarketID"`
		StationName   string `json:"StationName"`
		StarSystem    string `json:"StarSystem"`
		Items         []struct {
			StorageSlot   int    `json:"StorageSlot"`
			Name          string `json:"Name"`
			NameLocalised string `json:"Name_Localised"`
			StarSystem    string `json:"StarSystem"`
			MarketID      int64  `json:"MarketID"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}

	type loc struct {
		System string `json:"system"`
		Count  int    `json:"count"`
	}
	bySystem := map[string]int{}
	for _, it := range parsed.Items {
		bySystem[it.StarSystem]++
	}
	locs := make([]loc, 0, len(bySystem))
	for sys, count := range bySystem {
		locs = append(locs, loc{System: sys, Count: count})
	}

	out := map[string]any{
		"event_station":   parsed.StationName,
		"event_system":    parsed.StarSystem,
		"total_modules":   len(parsed.Items),
		"by_system":       locs,
	}
	return marshalOrNil(out)
}

// ─── StoredShips ──────────────────────────────────────────────────────────────

// compactStoredShips → totals + per-ship-type counts. The wire shape has
// both HomeShips (at current station) and ShipsRemote (elsewhere) arrays.
func compactStoredShips(raw json.RawMessage) (json.RawMessage, bool) {
	type shipEntry struct {
		ShipType      string  `json:"ShipType"`
		ShipTypeLocal string  `json:"ShipType_Localised"`
		Name          string  `json:"Name"`
		StarSystem    string  `json:"StarSystem"`
		StationName   string  `json:"StationName"`
		TransferPrice int64   `json:"TransferPrice"`
		TransferTime  int64   `json:"TransferTime"`
		Value         int64   `json:"Value"`
		Hot           bool    `json:"Hot"`
		InTransit     bool    `json:"InTransit"`
		ShipID        int64   `json:"ShipID"`
		StationMarket int64   `json:"MarketID"`
		Distance      float64 `json:"Distance"`
	}
	var parsed struct {
		StationName string      `json:"StationName"`
		StarSystem  string      `json:"StarSystem"`
		HomeShips   []shipEntry `json:"ShipsHere"`
		RemoteShips []shipEntry `json:"ShipsRemote"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}

	summarise := func(ships []shipEntry) []map[string]any {
		out := make([]map[string]any, 0, len(ships))
		for _, s := range ships {
			entry := map[string]any{
				"type":   friendlyName(s.ShipTypeLocal, s.ShipType),
				"system": s.StarSystem,
			}
			if s.StationName != "" {
				entry["station"] = s.StationName
			}
			if s.Name != "" {
				entry["name"] = s.Name
			}
			if s.InTransit {
				entry["in_transit"] = true
			}
			out = append(out, entry)
		}
		return out
	}

	out := map[string]any{
		"current_station": parsed.StationName,
		"current_system":  parsed.StarSystem,
		"here":            summarise(parsed.HomeShips),
		"remote":          summarise(parsed.RemoteShips),
	}
	return marshalOrNil(out)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// friendlyName picks the human-readable label when available, otherwise
// tidies the raw journal form ("$steel_name;" → "steel") so the model sees
// a word rather than markup.
func friendlyName(localised, raw string) string {
	if localised != "" {
		return localised
	}
	s := raw
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSuffix(s, "_name;")
	s = strings.TrimSuffix(s, ";")
	return s
}

// marshalOrNil compactly JSON-marshals v and signals success. Signature
// matches the eventCompactor contract.
func marshalOrNil(v any) (json.RawMessage, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return b, true
}
