# MR7 Tool Source Manifest

Status: **LOCAL SMOKE PASSED 2026-07-25; TOOL CONTRACT AMENDED 2026-07-27**

Every `ToolName` exposed by `internal/tools/executor.go` and
`internal/tools/tools_describe.go` is classified below. Current-galaxy and
raw-history rows are the MR7 smoke scope.

| Tool | Authoritative source | MR7 smoke |
|---|---|---|
| `status_service` | operations manager | out of scope |
| `restart_service` | operations manager | out of scope |
| `tail_logs` | operations manager | out of scope |
| `run_ansible` | operations manager | out of scope |
| `list_services` | operations manager | out of scope |
| `spansh_query` | external Spansh client | out of scope |
| `retrieve_carrier_route` | external Spansh client | out of scope |
| `galaxy_system` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_station` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_fleet_carrier` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_bodies` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_signals` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_surface_sites` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_power` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_faction` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_stats` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_query` | parser-restricted SQL through `galaxystore` as `galaxy_reader` | pass |
| `galaxy_market` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_expansion_check` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_nearby_powerplay` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_expansion_frontier` | `galaxy.*` through `galaxystore` | pass |
| `galaxy_history` | separate raw EDDN history connection | pass |
| `galaxy_powerplay_cycle` | separate raw EDDN history connection | pass |
| `galaxy_plasmium_buyers` | EDIN application mining maps plus `galaxy.*` | pass |
| `galaxy_ltd_buyers` | EDIN application mining maps plus `galaxy.*` | pass |
| `galaxy_expansion_targets` | EDIN application mining maps plus `galaxy.*` | pass |
| `galaxy_schema` | `galaxy.*` through `galaxystore` | pass |
| `bgs_guide_search` | checked-in BGS reference material | out of scope |
| `powerplay_guide_search` | checked-in Powerplay reference material | out of scope |
| `commander_events` | commander repository | out of scope |
| `commander_location` | commander repository | out of scope |
| `describe_tool` | static definitions in `internal/tools` | pass |

The checked-in integration test must pass with all three explicit connections:

- `GALAXY_TEST_DSN`: session `current_user` is `galaxy_reader`;
- `EDIN_TEST_DSN`: EDIN application database containing `kaine.mining_maps`;
- `EDDN_HISTORY_TEST_DSN`: raw EDDN database containing `feed.*`.

The smoke also asserts that `galaxy_query` can read `galaxy.system_catalog`,
cannot read `feed.messages`, and that history tools fail when the separate
history client is absent.

Local evidence:

- relational corpus: 1,320,332 `galaxy.system_catalog` rows;
- raw corpus: 62,953,840 `feed.messages` rows;
- application corpus: 153 `kaine.mining_maps` rows;
- `TestGalaxyRelationalToolsSmoke`: 23 subchecks passed in 22.46 seconds;
- no production or remote connection was used.

Post-MR7 amendment (2026-07-27): `system_profile` was removed as a duplicate,
incomplete broad lookup. `galaxy_system` now owns the complete compact
Markdown system inventory, while `galaxy_market` accepts one returned
`market_id` and emits the complete untruncated Markdown commodity snapshot.
The focused store/tool integration gate and the complete Go suite passed.
