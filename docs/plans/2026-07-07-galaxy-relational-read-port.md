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

## Remaining W5 Order

1. Record graph-era responses for the W5.7 contract harness before each cutover.
2. Port powerplay APIs/tools onto `galaxystore`. Done for W5.2 current-state surfaces.
3. Port Kaine system intel and watcher endpoints. Done for W5.3.
4. Port mining/expansion tools and surface-site radius query.
5. Port remaining MCP galaxy tools.
6. Implement `galaxy_query` with the parser-enforced SQL sandbox.
