package tools

// ToolGuidance maps tool names to detailed usage guidance.
// This guidance was formerly embedded in the system prompt and is now served
// on demand via the describe_tool meta-tool, saving ~4K tokens per turn.
var ToolGuidance = map[ToolName]string{
	ToolGalaxyMarket: `galaxy_market — Commodity market queries from Memgraph.

Usage modes:
1. Station inventory: system_name + station_name → all commodities at that station
2. System markets: system_name only → commodities from all stations in the system
3. Find buy locations: commodity + operation="buy" + optional filters
4. Find sell locations: commodity + operation="sell" + optional filters

Commodity names are auto-normalized (spaces removed, lowercased).

Parameters:
- commodity (string): e.g. "tritium", "platinum", "lowtemperaturediamond"
- operation (string): "buy" or "sell"
- system_name (string): get all market data for stations in this system
- station_name (string): search by station name (partial match)
- reference_system (string): calculate distances from this system (default: Sol)
- max_distance (number): max Ly from reference_system (default: 100)
- station_type (string): "orbital", "outpost", "planetary", or "any" (default)
- max_distance_ls (number): max station distance from star in ls
- min_pad (string): "L" (large only), "M" (medium+), "S" (any)
- min_price / max_price (number): price filters
- min_demand / min_stock (number): quantity filters
- limit (number): max results (default: 20, max: 100)
- exclude_carriers (bool): exclude fleet carriers (default: true)

IMPORTANT: galaxy_system and galaxy_station do NOT return market data — only whether a station HAS a market. Always use galaxy_market for actual prices.

Trading best practices:
- Large ships need L pads; ask if not specified
- Prefer orbital stations (Coriolis/Orbis/Ocellus) for regular trading
- Warn about stations >1000 ls from star (long supercruise)
- Present balanced options: price, distance, supercruise time, stock/demand
- Default: large pad, orbital preferred, <1000 ls, exclude carriers

Response format example:
"Best places to sell Platinum near Cubeo:
- **Chelomey Orbital** ([Cubeo](system://Cubeo)) — Coriolis, 35 ls from star
  Sell: 280,000 cr · Demand: 45,000 · 12 ly away"`,

	ToolGalaxyQuery: `galaxy_query — Ad-hoc Cypher queries against Memgraph.

Read-only only (MATCH/RETURN/WITH/WHERE/ORDER BY/LIMIT). LIMIT auto-appended if missing.

IMPORTANT: Before writing Cypher, call galaxy_schema to get the current node labels,
property names, edge types, and indexes. The schema evolves — don't assume property names.

Key properties (may change — verify with galaxy_schema):
- System: name, controlling_power, powerplay_state, reinforcement, undermining, x, y, z, location (point)
- Powerplay filtering: use s.controlling_power = 'Power Name' (property, not relationship)

CRITICAL: System.location is a spatial point with a point index (3M+ entries).
ALWAYS use point.distance() for distance/range queries — it uses the spatial index and is
1000x faster than manual sqrt on x/y/z properties.

Parameters:
- query (string, required): Cypher query
- parameters (object): $var substitution values

Distance calculation (FAST — uses spatial index):
MATCH (s1:System {name: 'Alrai'}), (s2:System {name: 'HIP 63499'})
RETURN point.distance(s1.location, s2.location) AS distance_ly

Range query (FAST — uses spatial index):
MATCH (ref:System {name: 'Sol'})
MATCH (s:System) WHERE point.distance(s.location, ref.location) <= 50
RETURN s.name, point.distance(s.location, ref.location) AS dist ORDER BY dist`,

	ToolGalaxyFaction: `galaxy_faction — Minor faction data from the galaxy database.

Search modes:
- By faction name: faction_name parameter
- By system: system_name → all factions in that system
- By state: faction_state → find factions in a given state across the galaxy

Parameters:
- faction_name (string): faction name for direct lookup
- system_name (string): system name to get all factions
- faction_state (string): "War", "Civil War", "Expansion", "Boom", "Bust", "Famine", "Outbreak", "Lockdown"
- include_systems (bool): include faction's systems when querying by name (default false)
- limit (number): max results (default 50)`,

	ToolGalaxyHistory: `galaxy_history — Historical powerplay data from EDDN raw feed (up to 30 days).

Returns daily reinforcement/undermining values, controlling power changes, and observation counts. Use for trend analysis, cycle comparisons, or investigating historical activity.

Parameters:
- system_name (string): single system name
- system_names (array of strings): multiple systems (max 10)
- days (number): history depth (default 14, max 30)`,

	ToolGalaxyPowerplayCycle: `galaxy_powerplay_cycle — Cycle-aware powerplay queries.

The powerplay tick occurs every Thursday 07:00 UTC. This tool aligns queries to cycle boundaries.

Parameters:
- system_name (string): single system name
- system_names (array of strings): multiple systems (max 10)
- cycle (number): 0=current week, -1=last week, -2=two weeks ago, etc. (default 0, min -8)
- compare (bool): if true and cycle=0, also returns previous cycle data and week-over-week deltas

Timing notes:
- Data from Thursday 07:00-08:30 UTC is unreliable (maintenance window)
- For ~2 hours after tick, some players report stale cached values
- Reinforcement resets to 0 at tick; control decay is applied

Example use cases:
- "How's Kaine doing this cycle?" → cycle=0
- "Compare to last week" → cycle=0 with compare=true
- "What changed at the tick?" → Compare cycle=-1 final values with cycle=0 start`,

	ToolGalaxyExpansionCheck: `galaxy_expansion_check — Validate expansion targets.

Checks if a system is within a power's control bubble:
- 20 Ly from nearest Fortified system
- 30 Ly from nearest Stronghold system

Parameters:
- system_name (string, required): system to check
- power_name (string): power to check for (default: "Nakato Kaine")`,

	ToolGalaxyNearbyPowerplay: `galaxy_nearby_powerplay — Powerplay activity near a system.

Returns nearby controlled systems (Fortified/Stronghold) and acquisition systems (Expansion/Contested) for a specific power within a given radius.

Parameters:
- system_name (string, required): reference system
- power_name (string): power to filter for (default: "Nakato Kaine")
- max_distance (number): search radius in Ly (default: 50, max: 100)`,

	ToolGalaxyExpansionFrontier: `galaxy_expansion_frontier — Systems on the edge of a power's control bubble.

Shows valid targets just inside range (can be expanded into now) and potential targets just outside (could become targets if nearby expansions succeed).

Parameters:
- control_system (string, required): a Fortified or Stronghold system
- power_name (string): power name (default: "Nakato Kaine")
- show (string): "inside", "outside", or "both" (default: "both")`,

	ToolGalaxyPlasmiumBuyers: `galaxy_plasmium_buyers — Mining intel for Platinum/Osmium buyers.

No parameters needed. Returns Boom-state stations near Kaine mining maps that buy Platinum/Osmium.

Scoring: >=1288t demand = optimal (100 pts Platinum, 80 pts Osmium), sub-threshold = linear scale, Military/Colony economy = 40 pts (hidden demand). Search radius: 20 Ly from Fortified, 30 Ly from Stronghold.`,

	ToolGalaxyLTDBuyers: `galaxy_ltd_buyers — Mining intel for Low Temperature Diamond buyers.

No parameters needed. Returns stations near Kaine mining maps scored by LTD demand and price.`,

	ToolGalaxyExpansionTargets: `galaxy_expansion_targets — Ranked expansion targets for Kaine.

No parameters needed. Returns systems within the control bubble (20 Ly of Fortified, 30 Ly of Stronghold) that are valid acquisition targets, scored by strategic value.`,

	ToolGalaxySystem: `galaxy_system — Query a star system from the EDIN galaxy database.

IMPORTANT: Always use the 'include' parameter to request only the sections you need.
Returning everything (bodies, stations, factions, signals, fleet carriers) produces very
large responses that consume context. For most queries you only need one or two sections.

Available sections for 'include':
- "system"          — Core system data: name, coordinates (x,y,z), population, government,
                      allegiance, controlling_power, powerplay_state, reinforcement, undermining
- "stations"        — All stations with type, services, distance from star, landing pads
- "bodies"          — Stars and planets with surface temp, gravity, rings, etc.
- "factions"        — Minor factions present with influence and state
- "signals"         — Biological/geological signal counts
- "fleet_carriers"  — Fleet carriers currently docked in the system

Parameters:
- system_name (string, required): Star system name (e.g. "Sol", "Cubeo")
- include (array of strings): Sections to return. Omit for all (use sparingly).

Common patterns:
- Coordinates / distance calc: include=["system"] — returns just coords + powerplay info
- Trading / docking info:      include=["system", "stations"]
- Mining / exploration:        include=["system", "bodies", "signals"]
- Full intel:                  omit include (or use system_profile tool instead)

For multi-system distance calculations, prefer galaxy_query with a Cypher distance formula
over calling galaxy_system 6 times.`,
}
