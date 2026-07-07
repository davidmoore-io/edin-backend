package tools

// ToolGuidance maps tool names to detailed usage guidance.
// This guidance was formerly embedded in the system prompt and is now served
// on demand via the describe_tool meta-tool, saving ~4K tokens per turn.
var ToolGuidance = map[ToolName]string{
	ToolGalaxyMarket: `galaxy_market — Commodity market queries from the relational galaxy database.

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

Graph schema — node types and their traversal paths:
  System  → HAS_BODY  → Body  → HAS_RING → Ring        (ring_class: Metallic/MetalRich/Rocky/Icy; reserve_level; has_ltd/has_tritium/has_painite)
  System  → HAS_STATION → Station → HAS_MARKET → Market
  Faction → PRESENT_IN → System   (rel props: influence, state, happiness)
  Power   → CONTROLS  → System
  Also: SystemSignal, Settlement, FleetCarrier, Shipyard, Outfitting, CodexEntry, Commodity

Key System props: name, slug, controlling_power, powerplay_state, reinforcement, undermining, location (spatial point), allegiance, last_eddn_update
Key Faction props: name, allegiance, government, is_pmf (true = Player Minor Faction, indexed), pmf_source
Key Ring props: ring_class, reserve_level, hotspot_types, has_ltd, has_tritium, has_painite

NEVER claim data is missing without querying. Ring types, station lists, and market data all exist — traverse the graph.
CRITICAL: Use point.distance() for spatial queries (indexed, 1000× faster than sqrt on x/y/z).
Powerplay: filter on s.controlling_power (property), not via relationships.
Staleness: s.last_eddn_update or s.last_event_time (TIMESTAMP).

Parameters:
- query (string, required): Cypher query
- parameters (object): $var substitution values`,

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

	ToolGalaxySurfaceSites: `galaxy_surface_sites — Surface-site radius search from the relational galaxy database.

Use for finding reported landable-surface sites near a reference system: Ancient Ruins, Biological Sites, geysers/fumaroles, visitor beacons, crash sites, and similar entries from EDDN ApproachSettlement data.

Parameters:
- system_name (string, required): reference system
- radius (number): search radius in Ly (default 100, max 500)
- site_kind (string): optional coarse kind filter, e.g. Ancient, Biological, Geological, VisitorBeacon, CrashSite
- name (string): optional reported-name filter, e.g. Ancient Ruins, Biological Site, Ice Geysers
- limit (number): max results (default 50, max 200)

Use this when a user asks for "systems within 100 Ly of X with Ancient Ruins / Biological Sites". Results include system, body, site name/kind, latitude/longitude, first_seen, last_seen, and distance_ly.`,

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

For multi-system distance calculations, prefer a dedicated galaxy tool (for example galaxy_surface_sites, galaxy_market, or galaxy_nearby_powerplay) when one fits. Use galaxy_query only for ad-hoc cases not covered by a dedicated tool.`,

	ToolBgsGuideSearch: `bgs_guide_search — Keyword search over the Elite Dangerous Background Simulation (BGS) reference guide. Returns ~2000-token text chunks around match clusters.

Use for questions about BGS rules and mechanics. NOT for live galaxy data — use galaxy_* tools for that.

Topics in the guide (use these as landmark search keywords):
  - BGS basics: systems, factions, reputation, influence, system states, sliders (economy, security), station news, leaderboards
  - Ticks: daily tick, weekly server maintenance tick
  - Squadron setup: earning money, BGS ship types, BGS plan, diplomacy, goals, system preparation, backfilling
  - Manipulation mechanics: daily scan, bucket model, ten levers, diminishing returns, influence distribution
  - Boosting/reducing influence via: missions, combat, exploration, trade, mining, smuggling, negative actions
  - Inducing states: Boom, Bust, War, Civil War, Election, Expansion, Retreat, Lockdown, Famine, Outbreak
  - Conflicts: government ethos, conflict table, coups, elections
  - Expansions: expansion diplomacy, detecting inactive PMFs
  - Retreat, Crime and Punishment

Parameters:
- query (string, required): Keyword or distinctive phrase. Case-insensitive, min 2 chars.
- max_chunks (number, optional): 1–5, default 3. Each chunk ~2000 tokens (~8000 chars).

Search behaviour:
- Matching is literal case-insensitive substring. Two-word phrases must appear verbatim.
- If a multi-word phrase returns 0 matches, RETRY with a single distinctive keyword.
- Results are ranked by match density: the first chunk is usually the main section for the topic.
- Chunks are nudged to line boundaries so they open/close cleanly.

!IMPORTANT — STRICT GROUNDING RULE:
When you use this tool, you MUST answer using ONLY the text contained in the returned chunks.
Do NOT assume, presuppose, infer, extrapolate, or draw on any other knowledge about Elite
Dangerous BGS, even if you believe you know the answer. If the returned chunks do not contain
enough information to answer the user's question, say so explicitly and offer to search with
a different keyword. Quote or paraphrase the chunks directly; do not embellish.`,

	ToolPowerplayGuideSearch: `powerplay_guide_search — Keyword search over the Elite Dangerous Powerplay Reference Card. Returns ~1000-token text chunks around match clusters.

Use for questions about Powerplay activity rules, merit/CP modifiers, control point thresholds, system types, and Stronghold Carrier mechanics. NOT for live galaxy data — use galaxy_* tools (galaxy_power, galaxy_powerplay_cycle, galaxy_nearby_powerplay, etc.) for current cycle state, who controls a system, etc.

Topics in the refcard (use these as landmark search keywords):
  - Activity Requirements (CP thresholds): "120000", "350000", "650000", "1000000", "fortified threshold", "stronghold threshold"
  - Weekly tasks: "weekly tasks", "rank 40", "assignments"
  - System types: "supporting system", "acquisition system", "reinforcement system", "undermining system", "friendly system", "unfriendly system"
  - Activities (look up by name): "bounty hunting", "exobiology", "exploration data", "scan datalinks", "transport power commodity", "upload malware", "sell mined resources", "sell rare goods", "holoscreen hacking", "power kills", "commit crimes", "collect escape pods", "complete support missions", "flood markets"
  - Modifiers: "ethos bonus", "system strength penalty", "beyond frontline", "system rank penalty", "stronghold carrier nullification", "general undermining bonus", "focused undermining bonus", "general reinforcement penalty", "resistance reinforcement", "overkill reinforcement", "emergency defence bonus", "acquisition follow-through", "assignment completion", "exploration data exchange rate"
  - Stronghold Carriers: "+SC", "-SC", "stronghold carrier"

Parameters:
- query (string, required): Keyword or distinctive phrase. Case-insensitive, min 2 chars.
- max_chunks (number, optional): 1–5, default 2. Each chunk ~1000 tokens (~4000 chars). The corpus is small so 1-2 chunks usually covers a topic.

Search behaviour:
- Matching is literal case-insensitive substring. Two-word phrases must appear verbatim.
- If a multi-word phrase returns 0 matches, RETRY with a single distinctive keyword.
- Results are ranked by match density: the first chunk is usually the main section for the topic.
- Chunks are nudged to line boundaries so they open/close cleanly.

!IMPORTANT — STRICT GROUNDING RULE:
When you use this tool, you MUST answer using ONLY the text contained in the returned chunks.
Do NOT assume, presuppose, infer, extrapolate, or draw on any other knowledge about Elite
Dangerous Powerplay, even if you believe you know the answer. If the returned chunks do not
contain enough information to answer the user's question, say so explicitly and offer to
search with a different keyword. Quote or paraphrase the chunks directly; do not embellish.`,

	ToolCommanderEvents: `commander_events — Query the commander's synced Elite Dangerous journal events.

Returns events with timestamp, event_type, and event_data payload. Use event_types to filter
efficiently — always filter when you know what you're looking for.

Key event types and their useful fields:

LOCATION / TRAVEL
- Docked: StationName, StarSystem, StationType, MarketID, StationFaction, DistFromStarLS
- FSDJump: StarSystem, JumpDist, FuelUsed, FuelLevel, SystemAllegiance, Powers
- Location: StarSystem, StationName (if docked on login)
- Undocked: StationName, StarSystem

COLONISATION
- ColonisationConstructionDepot: fires when the commander opens a construction depot UI.
  Compacted payload: {market_id, progress (0.0–1.0), complete, failed,
  resources:[{name, required, provided, remaining, payment}]}
  This IS the shopping list. Always query this when asked about construction depot needs,
  resource requirements, or what a colony needs. Filter: event_types=ColonisationConstructionDepot.
  The most recent event reflects current depot state (resources update as deliveries are made).

CARGO / TRADE
- Cargo: current cargo manifest — {vessel, total, items:[{name, count}]}
- MarketBuy / MarketSell: commodity, count, price, market_id, station

MISSIONS
- MissionAccepted: Name, Faction, Reward, Expiry, DestinationSystem, DestinationStation
- MissionCompleted / MissionFailed / MissionAbandoned: MissionID, Name, Reward

SHIP
- Loadout: ship type, name, ident, cargo capacity, jump range (modules elided in compacted form)
- StoredShips / StoredModules: where the commander's ships and modules are stored

Common patterns:
- "What does this construction depot need?" → event_types=ColonisationConstructionDepot, limit=1
- "Where am I?" → event_types=Docked,FSDJump,Location, limit=5
- "What's in my cargo?" → event_types=Cargo, limit=1
- "Recent activity" → no filter, limit=20

NEVER claim that journal data is unavailable without querying first. If a ColonisationConstructionDepot
event is not present, the commander has not yet opened the depot UI in-game — say so, and ask them
to approach and open the depot panel so the journal fires the event.

POWERPLAY
- Powerplay: fires at game startup. Fields: Power (name), Rank (1–50), Merits (lifetime total),
  TimePledged (seconds). Use for "what power am I pledged to?", "what's my rank?", current merit baseline.
  Filter: event_types=Powerplay, limit=1.
- PowerplayMerits: fires each time merits are earned. Fields: Power, MeritsGained, TotalMerits.
  Use for "how many merits did I earn this session?" — sum MeritsGained since last Powerplay event,
  or diff TotalMerits between earliest and latest. Filter: event_types=PowerplayMerits.
- PowerplayRank: fires on rank change. Fields: Power, Rank (new rank).
  Filter: event_types=PowerplayRank, limit=1.
Common patterns:
- "What's my merit count?" → event_types=Powerplay, limit=1 (for baseline) then event_types=PowerplayMerits (for session gains)
- "Did I rank up?" → event_types=PowerplayRank, limit=5

COLONISATION (continued)
- ColonisationContribution: fires when the commander delivers goods to a construction depot.
  Fields: contributions:[{name, nameLocalised, amount}]. Use this to answer "what did I just deliver?"
  or "what have I delivered this session?". Pair with ColonisationConstructionDepot to show
  remaining requirements after deliveries. Filter: event_types=ColonisationContribution.

MATERIALS (for engineering / synthesis)
- Materials: bulk inventory fired at startup. Contains Raw, Manufactured, Encoded arrays, each with
  {Name, Count} entries. Use for "what materials do I have?" / "do I have enough X to craft Y?".
  Filter: event_types=Materials, limit=1.
- MaterialCollected: single collection event. Fields: Category (Raw/Manufactured/Encoded), Name, Count.
- MaterialDiscarded: Fields: Category, Name, Count.
- MaterialTrade: traded at a material trader. Fields: TraderType, Paid {Material, Category, Quantity},
  Received {Material, Category, Quantity}.
Common pattern: "What materials do I have?" → event_types=Materials, limit=1

FLEET CARRIER
- CarrierStats: fired when carrier panel opened. Key fields: Callsign, Name, FuelLevel (tonnes),
  JumpRangeCurr (LY), JumpRangeMax (LY), Finance:{CarrierBalance, ReserveBalance, AvailableBalance},
  SpaceUsage:{FreeSpace (tonnes)}. Use for carrier status questions.
  Filter: event_types=CarrierStats, limit=1.
- CarrierLocation: Fields: StarSystem. Use for "where is my carrier?".
  Filter: event_types=CarrierLocation,CarrierJump, limit=1.
- CarrierJumpRequest: Fields: SystemName, Body (optional), DepartureTime. Pending jump.
  Filter: event_types=CarrierJumpRequest, limit=1.
- CarrierJumpCancelled: jump was cancelled.
- CarrierJump: carrier completed a jump. Fields: StarSystem, StationName, StationType, MarketID, Population.
Common patterns:
- "Where's my carrier?" → event_types=CarrierLocation,CarrierJump, limit=1
- "Is my carrier jumping?" → event_types=CarrierJumpRequest,CarrierJumpCancelled, limit=5

NAVIGATION
- NavRoute: fired when a route is plotted or cleared. Fields: Route:[{StarSystem, StarClass}].
  Route is empty when cleared. Use for "where am I headed?" / "how many jumps to destination?".
  Filter: event_types=NavRoute, limit=1.
- StartJump: fired as FSD spools up. Fields: JumpType (Hyperspace/Supercruise), StarSystem, StarClass.

COMBAT EARNINGS
- Bounty: Fields: TotalReward (credits), VictimFaction, Target (ship type). Combat kill bounty.
- FactionKillBond: Fields: reward (credits), awardingFaction. CZ kill bond.
Common pattern: "How much did I earn from that fight?" → event_types=Bounty,FactionKillBond, limit=20`,
}
