# Galaxy Relational Read Port — W5 Work Note

Authoritative delivery plan: `edin-data/docs/plans/galaxy-relational-delivery-plan.md`.
This note is the backend-local execution record for Wave 5.

## Scope

Port read consumers from Memgraph to the `galaxy.*` relational schema without changing
frontend routes, HTTP JSON shapes, or MCP tool schemas.

W5.1 is the foundation slice:

- `internal/galaxystore` owns relational galaxy reads.
- Core system-detail query joins `system_catalog`, `system`, `system_power`,
  `system_faction`, and `faction`.
- Name lookup and no-space slug lookup use the approved relational indexes.
- The package exposes graph-era-compatible JSON field names but does not import
  `internal/memgraph`.

## Why

Every later surface needs the same system identity and current-state facts. Centralising
that SQL prevents each endpoint/tool from inventing its own join, timestamp rule, or slug
rule.

## Safety While W4 Runs

- Read-port work must not mutate `feed.messages` or `galaxy.*` outside isolated test
  transactions.
- No endpoint cutover is accepted without focused contract tests.
- Memgraph code may be read as a legacy response-shape reference only; new relational
  code must not call it.

## W5.1 Evidence

- Fast package tests: `GOWORK=off go test ./internal/galaxystore`
- Optional real-Postgres test: `GOWORK=off GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test ./internal/galaxystore`
- Backend compile check: `GOWORK=off go test ./...`

## W5.2 Evidence

- Added relational powerplay reads to `internal/galaxystore`.
- Wired `/api/edin/powerplay`, `/api/edin/hip-thunderdome`, power-standings
  state-count enrichment, and the `galaxy_power` MCP tool to the relational store.
- The EDDN raw pool now constructs the relational store for `control-api`; no new
  configuration surface was added.
- Intentional diagnostic value change: top-level source labels on the new current-state
  path say Postgres/relational rather than Memgraph.
- Tests: `GOWORK=off GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test ./internal/galaxystore`
- Tests: `GOWORK=off go test ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api`
- Tests: `GOWORK=off go test ./...`

## W5.3 Evidence

- Added relational system-directory/search reads to `internal/galaxystore`:
  system autocomplete, station autocomplete, exact system detail, slug-keyed watch
  snapshot, station market, carrier market, and station-market faction-state
  enrichment.
- Rewired Kaine system-intel/directory HTTP handlers to use `galaxyStore` instead
  of `memgraph` for:
  `/api/kaine/systems/search`, `/api/kaine/search`,
  `/api/kaine/systems/{name}`, `/api/kaine/systems/intel/{name}`,
  `/api/kaine/watcher/systems/{slug}`, `/api/kaine/market/station`, and
  `/api/kaine/market/carrier`.
- Left raw-EDDN-backed traffic/history/event/market-history sections unchanged;
  those are not Memgraph consumers.
- Query tuning evidence on local W4 data: system autocomplete uses
  `idx_catalog_name_prefix` (~4.6 ms for `Sol`), station autocomplete uses
  `idx_station_name_trgm` (~0.4 ms for `Jameson`), and station market lookup
  resolves system id first then uses `idx_station_system` (~0.3 ms for
  `Sol`/`Daedalus`).
- Route-level unauthenticated smoke is not representative because the existing
  Kaine auth middleware still requires a JWT validator even when the smoke process
  sets `KAINE_AUTH_ENABLED=false`; this task did not change auth semantics.
- Tests: `GOWORK=off GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test -count=1 ./internal/galaxystore`
- Tests: `GOWORK=off go test ./internal/galaxystore ./internal/httpapi ./cmd/control-api`
- Tests: `GOWORK=off go test ./...`

## W5.4 Evidence

- Imported the production `kaine.mining_maps` application table into local
  `edin-timescaledb` for realistic mining-map checks: 153 rows, including 72
  Plasmium maps and 25 LTD maps by commodity tags. This import touched only the
  EDIN app database, not `feed.messages` or `galaxy.*`.
- Rewired Kaine mining-map list/stats/import validation to enrich/validate live
  system state through `galaxy.*` instead of Memgraph.
- Rewired mining intelligence HTTP routes and MCP tools to relational reads:
  Plasmium buyers, LTD buyers, expansion targets, and survey export.
- Added a guarded relational mining smoke test that executes all four mining
  workflows against local `edin-timescaledb` + `eddn-timescaledb` when
  `EDIN_TEST_DSN` and `GALAXY_TEST_DSN` are set.
- Data-shape correction found during local setup: the tracked EDIN init SQL and
  CSV importer still used the old `map_url`/`map_url_2`/`power_state` shape,
  while production and backend code already used `map_1`/`map_2`/`map_3` plus
  live power-state enrichment. The init/migration/import scripts were aligned
  to the production shape.
- Follow-up correction: Plasmium/LTD buyer candidates explicitly exclude
  construction/colonisation depots (`SpaceConstructionDepot`, `space_depot`, or
  `colonisationcontribution` service). These locations accept construction
  requirements but are not Kaine commodity sell sites.
- Tests: `python3 -m pytest scripts/data-import/test_import_mining_maps.py`
  in `edin-data`.
- Tests: `GOWORK=off EDIN_TEST_DSN=postgres://edin_admin:eddn-local-dev@localhost:5432/edin GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test -count=1 -run TestRelationalMiningSmoke -v ./internal/kaine`
- Tests: `GOWORK=off go test ./internal/kaine ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api`
- Tests: `GOWORK=off go test ./...`

## W5.5 Evidence

- Rewired the remaining `galaxy_*` MCP read tools that were still calling
  Memgraph: `galaxy_system`, `galaxy_station`, `galaxy_fleet_carrier`,
  `galaxy_bodies`, `galaxy_signals`, `galaxy_market`,
  `galaxy_expansion_check`, `galaxy_nearby_powerplay`,
  `galaxy_expansion_frontier`, `galaxy_faction`, `galaxy_stats`, and
  `galaxy_schema`.
- Added the new `galaxy_surface_sites` MCP tool for radius/kind/name searches
  over `galaxy.surface_site` joined to `galaxy.system_catalog`, proving the W4
  corrective entity is queryable for Ancient/Biological/Geological/Visitor
  Beacon/Crash Site style questions.
- Removed W5.5-owned Memgraph fallbacks rather than dual-reading. The remaining
  tools package graph dependency is the explicit W5.6 holdback:
  `galaxy_query`/Cypher plus executor wiring for the still-present client.
- Ported `system_profile` to `galaxystore` as a W5.5 cleanup because it is a
  galaxy-read user surface even though it is not named `galaxy_*`.
- Intentional diagnostic value change continues: relational paths report
  `source: postgres`.
- Tests: `GOWORK=off GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test -count=1 -run TestGalaxyRelationalToolsSmoke -v ./internal/tools`
- Tests: `GOWORK=off go test ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api`
- Tests: `GOWORK=off go test ./...`

## W5.6 Evidence

- Re-issued `galaxy_query` from Cypher/Memgraph to parser-validated PostgreSQL
  SQL against `galaxy.*`.
- Added and pinned `github.com/pganalyze/pg_query_go/v6 v6.2.2` for AST-based
  validation. The validator rejects multi-statement input, non-SELECT top-level
  statements, DDL, DML, COPY, CALL, DO, SET/RESET/SHOW, transaction control,
  EXPLAIN/ANALYZE, LOCK, LISTEN/NOTIFY, PREPARE/EXECUTE, row-locking SELECTs,
  data-modifying CTEs, and known side-effect/delay functions.
- The tool executes validated SQL inside a tool-created read-only transaction,
  sets local `statement_timeout=2s`, `work_mem=16MB`, and
  `default_transaction_read_only=on`, uses an application context timeout, and
  wraps every query with a deterministic `LIMIT 100` cap.
- Parameters use PostgreSQL-native `$1`, `$2`, ... placeholders with the MCP
  `parameters` object keyed as `"1"`, `"2"`, ... for deterministic binding.
- Removed Memgraph wiring from `internal/tools.Executor`; the tools package no
  longer imports or receives a Memgraph client. Server-level legacy Memgraph
  routes remain outside W5.6 and are still tracked for W8 retirement/porting.
- Updated `galaxy_query` MCP definition and `tool_guidance.go` from graph/Cypher
  guidance to relational SQL guidance.
- Tests: `GOWORK=off GALAXY_TEST_DSN=postgres://eddn_admin:eddn-local-dev@localhost:5433/eddn_raw go test -count=1 -run TestGalaxyRelationalToolsSmoke -v ./internal/tools`
- Tests: `GOWORK=off go test ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api`
- Tests: `GOWORK=off go test ./...`

## Remaining W5 Order

1. Record graph-era responses for the W5.7 contract harness before each cutover.
2. Port powerplay APIs/tools onto `galaxystore`. Done for W5.2 current-state surfaces.
3. Port Kaine system intel and watcher endpoints. Done for W5.3.
4. Port mining/expansion tools. Done for W5.4.
5. Port remaining MCP galaxy tools and surface-site radius query. Done for W5.5.
6. Implement `galaxy_query` with the parser-enforced SQL sandbox. Done for W5.6.
