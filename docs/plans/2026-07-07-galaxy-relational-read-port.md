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

## Remaining W5 Order

1. Record graph-era responses for the W5.7 contract harness before each cutover.
2. Port powerplay APIs/tools onto `galaxystore`.
3. Port Kaine system intel and watcher endpoints.
4. Port mining/expansion tools and surface-site radius query.
5. Port remaining MCP galaxy tools.
6. Implement `galaxy_query` with the parser-enforced SQL sandbox.
