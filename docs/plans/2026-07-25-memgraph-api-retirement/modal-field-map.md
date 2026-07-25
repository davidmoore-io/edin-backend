# Modal Field Map

This is the MR0 contract for the two surviving powerplay modal sub-actions.
Shape comes from the legacy `memgraph.FactionPresence` and
`memgraph.StationData` DTOs and their handlers. Values come from independently
seeded relational fixtures.

## Factions

| JSON field | Relational expression | Null/zero rule |
|---|---|---|
| `faction_name` | `galaxy.faction.name` | always emitted |
| `system_name` | requested exact-case system name | always emitted |
| `influence` | `galaxy.system_faction.influence::float8` | always emitted, including zero |
| `state` | `galaxy.system_faction.state` | omitted only when empty |
| `active_states` | `galaxy.system_faction.active_states` | omitted when empty |
| `pending_states` | `galaxy.system_faction.pending_states` | omitted when empty |
| `happiness` | `galaxy.system_faction.happiness` | NULL maps to empty and is omitted |
| `last_event_time` | `galaxy.system_faction.last_event_time` | always emitted; zero `time.Time` encodes as year 1 |

Rows resolve through exact `galaxy.system_catalog.name = $1`, then order by
influence descending and faction name ascending. Unknown and wrong-case names
return `[]`.

## Stations

| JSON field | Full station expression | Stub expression | Null/zero rule |
|---|---|---|---|
| `id64` | `station.market_id` | `0::bigint` | always emitted |
| `name` | `station.name` | `station_stub.name` | always emitted |
| `type` | `station.station_type` | `station_stub.type` | NULL/empty omitted |
| `system_name` | requested exact-case name | requested exact-case name | always emitted |
| `distance_ls` | `COALESCE(station.dist_from_star_ls,0)` | `0` | zero omitted |
| `max_pad` | L/M/S from pad counts | empty | empty omitted |
| `is_planetary` | `false` for `kind='station'` | `false` | false omitted |
| `services` | `station.services` | empty array | empty omitted |
| `controlling_faction` | joined `faction.name` | empty | empty omitted |
| `has_market` | matching `galaxy.market` exists | false | false omitted |
| `has_shipyard` | matching `galaxy.shipyard` exists | false | false omitted |
| `has_outfitting` | matching `galaxy.outfitting` exists | false | false omitted |
| `last_eddn_update` | `station.last_event_time` | `station_stub.last_event_time` | always emitted |

`system_id64` remains zero in the dedicated modal DTO and is therefore omitted.
Only `galaxy.station.kind = 'station'` is eligible; construction depots are
excluded. Stub/full collisions are resolved by station name with the full row
winning. Rows order by distance ascending with relational NULL represented as
zero (therefore first), then name. The legacy post-query Fleetcarrier filter is
retained even though relational carrier rows are not present in either source.

## Wrappers

```json
{"system_name":"Wolf 1060","factions":[]}
```

```json
{"system_name":"Wolf 1060","stations":[]}
```
