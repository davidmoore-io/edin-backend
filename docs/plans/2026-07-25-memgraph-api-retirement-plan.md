# Memgraph API Retirement Plan

Date: 2026-07-25

Status: **DRAFT FOR ADVERSARIAL REVIEW - NO EXECUTION AUTHORISED**

Owners:

- implementation: `edin-backend`
- database contract and migrations: `edin-data`
- deployment and runtime removal: `edin-backend/ansible` and `atlas`
- acceptance: David

Authoritative parent plans:

- `edin-data/docs/plans/galaxy-relational-delivery-plan.md`
- `edin-data/docs/plans/2026-07-11-new-server-migration-runbook.md`

This is the zero-decision execution plan for removing every remaining
production Memgraph-bound API path. It closes the surfaces previously accepted
as broken in W7 and makes their closure a prerequisite for the new server's N7
dark application verification.

## 0. Verified Baseline

As of 2026-07-25:

- W5.1-W5.6 are delivered. `internal/tools` no longer imports or receives a
  Memgraph client, and `galaxy_query` is PostgreSQL SQL.
- the live listener, galaxy writer, and relational database run on
  `new-edin-space`; the application stack remains behind maintenance.
- the powerplay current-state list is relational, but its modal's `factions`
  and `stations` calls still return 503 when Memgraph is disabled.
- all five handlers in `internal/httpapi/galaxy.go` still call Memgraph.
- `internal/httpapi/survey_route.go` still creates Neo4j sessions directly.
- `control-api` still carries optional Memgraph construction, a powerplay
  fallback, and a Memgraph diagnostic even though new production deliberately
  has no Memgraph service.
- the previous delivery/runbook language intentionally accepted these failures
  until W8. This plan supersedes that exception and moves closure before N7.

## 1. Objective

Move every remaining live HTTP read from Memgraph to PostgreSQL `galaxy.*`,
preserving its route, authentication, status-code behaviour, JSON field names,
null/empty-array behaviour, ordering, limits, cache headers, and user-visible
semantics.

The final state is:

1. `control-api` does not construct, receive, probe, or fall back to a
   Memgraph client.
2. Every current-galaxy HTTP read uses `internal/galaxystore` through the
   `galaxy_reader` connection.
3. Every MCP current-galaxy tool remains on `internal/galaxystore`; historical
   tools keep their separate read-only raw-feed connection.
4. The galaxy static-data exporter reads `galaxy.*`, so enabling it cannot
   resurrect Memgraph.
5. production configuration, diagnostics, sidecar allowlists, and deployment
   templates contain no active Memgraph dependency.
6. the new host can pass N7 with `MEMGRAPH_ENABLED` absent, not merely false.

## 2. Scope Boundary

### 2.1 In scope

| Surface | Current Memgraph dependency | Required disposition |
|---|---|---|
| `GET /api/edin/systems/{name}/factions` | `GetFactionsInSystem` | port to `galaxy.system_faction` + `galaxy.faction` |
| `GET /api/edin/systems/{name}/stations` | `GetStationsInSystem` | port to `galaxy.station` + related current-state tables |
| `GET /api/galaxy/view` | `GetSystemsInBounds` | port to the `system_catalog` cube index and current system/power joins |
| `GET /api/galaxy/system/{systemId64}` | `GetSystemDetail` | port complete system/body/ring/station projection |
| `GET /api/galaxy/system/name/{systemName}` | `GetSystemDetailByName` | same projection after exact case-sensitive name resolution |
| `GET /api/galaxy/search` | `SearchSystemsByPrefix` | port to `idx_catalog_name_prefix` and current-state joins |
| `GET /api/galaxy/stats` | `GetGalaxyViewStats` | port aggregate counts to `galaxy.*` |
| `GET /api/internal/survey-route` | direct Neo4j sessions | port anchor/candidate lookups to relational spatial SQL |
| `POST /admin/diagnose` check `memgraph` | Memgraph process and Cypher probe | replace with `galaxy-reader` relational probe |
| powerplay cache refresh | relational first, Memgraph fallback | delete fallback; retain stale cache on relational failure |
| `cmd/galaxy-exporter` | `GetAllSystemsMinimal` | port static export source to ordered relational streaming |
| `cmd/control-api` and `httpapi.Server` | constructs and carries Memgraph client | remove wiring after all handlers are relational |
| production Ansible/config | dormant Memgraph switches and labels | remove active configuration and deployment paths |

### 2.2 Explicitly out of scope

These are not Memgraph migrations and MUST NOT be folded into this work:

- `/api/edin/systems/{name}/history` and `expansion-history`; these are
  time-series reads from `feed.powerplay_hourly` through the raw EDDN pool.
- Kaine objectives, chat, users, groups, prompts, and mining-map ownership;
  these belong to the EDIN application database.
- commander ingest, commander history, Frontier auth, and Copilot sessions.
- Spansh, EDSM, static guide search, operations tools, DayZ, and external APIs.
- any schema or writer-semantic change.
- deletion of old Memgraph volumes or frozen-old-server data.

### 2.3 MCP disposition

MCP is a verification task, not an implementation rewrite. W5.5 and W5.6
already removed Memgraph from `internal/tools`.

| MCP family | Authoritative source after this plan |
|---|---|
| current galaxy, system, station, carrier, body, signal, market, faction, power, expansion, mining, surface-site, stats, schema | `galaxy.*` through `galaxystore` |
| `galaxy_query` | parser-restricted SQL under `galaxy_reader` |
| `galaxy_history`, `galaxy_powerplay_cycle` | raw EDDN history connection |
| commander tools | commander repository |
| guides | checked-in reference material |
| Spansh/route tools | external clients |
| operations tools | operations manager |

No MCP tool may receive a Memgraph client or import `internal/memgraph`.

## 3. Non-Negotiable Contracts

### MR-D1: One current-state store

`internal/galaxystore` owns all production reads from `galaxy.*`. HTTP handlers,
MCP tools, background refreshes, and exporters must not embed parallel SQL
implementations when the same projection is shared.

### MR-D2: Existing wire contracts stay stable

The frontend does not change to accommodate this migration. Existing routes,
query parameters, defaults, caps, HTTP status classes, JSON names, omitted
fields, array ordering, and cache headers remain stable.

Where the graph implementation returned nondeterministic collection order, the
relational implementation pins deterministic domain order and records it in
tests:

- factions: influence descending, faction name ascending;
- stations: distance from arrival ascending with nulls last, then name;
- bodies: body id ascending;
- rings: ring name ascending;
- hotspot names: commodity name ascending;
- search: population descending, then name, then id64;
- viewport rows: id64 ascending before `LIMIT`.

All successful empty collections encode as `[]`, never `null`.

### MR-D3: Read roles remain separated

`GALAXY_READER_DSN` is the only connection used for `galaxy.*`. It must connect
as `galaxy_reader`, with no `feed.*` privilege.

Raw history retains its own read-only raw-feed connection. This plan must not
grant `feed` to `galaxy_reader` to make history endpoints convenient.

### MR-D4: No fallback to Memgraph

After a handler is ported, its Memgraph fallback is deleted in the same commit.
A relational query error is observable and preserves existing stale cache where
that cache exists; it never silently switches databases.

### MR-D5: Query plans are gates

Every new query is run against the new production-shaped database with
`EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS)` after `ANALYZE`.

API transactions also retain the reader safety floor:

- role `statement_timeout`: 15 seconds;
- `galaxy_query`: 2 seconds;
- read-only transaction where multiple statements form one projection;
- no sequential scan of `galaxy.system_catalog` for bounded spatial or prefix
  lookups;
- no per-row body, ring, hotspot, station, or faction query.

### MR-D6: No live deployment until the complete set is ready

MR0-MR7 build and test locally. MR8 is one dark `control-api` deployment with
all route ports, MCP verification, diagnostics changes, and Memgraph wiring
removal together. There is no mixed production mode.

### MR-D7: Historical Memgraph code is not production code

During implementation, `internal/memgraph` may be read only as a legacy
response-shape reference. New code cannot import it.

After contract fixtures no longer depend on it, remove the package and local
Memgraph development stack in MR9. Historical plans may retain references.

## 4. Pinned Relational Projections

### 4.1 Powerplay modal sub-actions

Add exported methods to `galaxystore`:

```go
GetFactionsInSystem(ctx context.Context, systemName string) ([]FactionPresence, error)
GetStationsInSystem(ctx context.Context, systemName string) ([]StationData, error)
```

Both resolve `system_catalog.id64` by the exact system name supplied, matching
the graph pattern `{name: $name}`.
Unknown systems return an empty slice to preserve the current sub-action
contract. They reuse the existing private relational projections; do not call
`GetSystemFull` and load unrelated slices.

The `stations` handler retains its exact current post-query filter:
case-insensitive `Fleetcarrier` is removed. Do not add another filter in this
port. (`/api/galaxy/system/*` separately excludes both `Fleetcarrier` and
`Drake-Class Carrier`, matching its own legacy query.)

The handler maps relational rows into a dedicated wire DTO matching the MR0
legacy fixture. In particular, the current graph sub-action sets
`system_name` but leaves `system_id64` omitted; directly encoding the broader
`galaxystore.StationData` would add a field and is forbidden. Faction rows
likewise echo the requested `system_name`.

The HTTP handler keeps the existing wrappers:

```json
{"system_name":"Wolf 1060","factions":[]}
```

```json
{"system_name":"Wolf 1060","stations":[]}
```

### 4.2 Galaxy viewport

Move graph-era viewport request/response types out of `internal/memgraph` into
`internal/galaxystore` or an HTTP contract package. The SQL source is:

- coordinates and identity: `galaxy.system_catalog`;
- population/allegiance: `galaxy.system`;
- controlling power/state: `galaxy.system_power`.

The bounding predicate must use `idx_catalog_loc`:

```sql
cube(ARRAY[c.x, c.y, c.z]) <@
cube(ARRAY[$1, $2, $3], ARRAY[$4, $5, $6])
```

and require all three coordinates non-null. Optional `power`, `allegiance`,
and `state` filters are parameterized. Run one count query and one deterministic
limited data query inside one read-only repeatable-read transaction so
`total_count`, `systems`, and `truncated` describe one snapshot.

Existing guards remain:

- all six bounds required;
- minimum must not exceed maximum;
- maximum 10,000 ly per dimension;
- default limit 50,000;
- hard cap 100,000.

### 4.3 Galaxy system detail

Both ID and name routes call one `galaxystore.GetGalaxySystemDetail` projection.
Name resolution is exact and case-sensitive, matching the existing graph
query. Unknown identity returns the existing 404.

The response remains:

```json
{"system":{},"bodies":[],"stations":[]}
```

Source map:

| Output | Source |
|---|---|
| system identity/coordinates | `system_catalog` |
| population/allegiance/government/economies/security/faction/timestamps | `system`, controlling `faction`, controlling `system_faction` |
| power/state | `system_power` |
| bodies | `body`, including typed extraction from `physical` |
| rings | `ring` |
| hotspot arrays/flags | `ring_hotspot` + `commodity` |
| stations | `station` + controlling `faction` |

Before coding, add
`docs/plans/2026-07-25-memgraph-api-retirement/system-detail-field-map.md`.
It must enumerate every field in the legacy `SystemInfo`, `BodyInfo`,
`RingInfo`, and `StationInfo` types, its exact relational expression, unit
conversion if any, null rule, and JSON omission rule. An unmapped field is a
hard stop, not permission to emit zero.

Body, ring, hotspot, and station collections are each fetched set-wise. The
implementation may use multiple bounded queries in one transaction; it may not
issue a query per body or ring.

### 4.4 Search and stats

Search uses `lower(c.name) LIKE lower($1) || '%'`, the
`idx_catalog_name_prefix` contract, and the existing limit rules (default 10,
accepted range 1-50). It enriches current values through left joins and returns
the existing `{systems,count}` wrapper.

Stats use:

- `count(*)` from `system_catalog`;
- `sum(population)` from `system`;
- `count(*)` from `power`;
- `count(*)` from `station`, excluding the two carrier station types.

Because exact full counts are not request-path work, implement a process-local
snapshot refreshed at startup and no more than once every six hours. The
existing response `Cache-Control: public, max-age=60` remains unchanged; that
header controls client caching, not the internal aggregate cadence. Refresh
uses one read-only transaction, runs asynchronously after startup, and is
single-flight. Refresh errors retain the previous snapshot and expose an error
metric; a request before the first successful snapshot returns the existing
500 status class.

### 4.5 Survey route

Keep route/auth/query parameters/response unchanged. Replace direct Neo4j
sessions with one `galaxystore` method that accepts:

- mining-map system names;
- optional start system;
- result limit.

The method performs:

1. resolve all map anchors in one query;
2. retain anchors only when current powerplay state is `Fortified` (20 ly) or
   `Stronghold` (30 ly);
3. find all candidate systems with population greater than zero and at least
   one large-pad station of type `Coriolis`, `Orbis`, `Ocellus`, `Dodec`, or
   `Asteroidbase`;
4. use the `system_catalog` cube index for each radius via a lateral/values
   anchor relation and apply exact Euclidean distance after the cube
   prefilter;
5. deduplicate candidates by id64;
6. aggregate and order qualifying stations by distance then name;
7. derive `last_update` exactly as the existing relational system projection:
   `GREATEST(system.last_event_time, system.last_faction_update,
   system_power.last_event_time)`, ignoring nulls through the existing
   `-infinity`/`NULLIF` expression;
8. preserve the current stale-first selection and Go nearest-neighbour route.

An unknown explicit `start` remains HTTP 400. Missing mining-map anchors remain
a successful empty response.

The SQL must be one set-based candidate query, not one query per mining-map
anchor.

### 4.6 Diagnostics

Replace the request check name `memgraph` with `galaxy-reader`.

`galaxy-reader`:

- inspects container `eddn-timescaledb`;
- runs a bounded query through the `GALAXY_READER_DSN` pool;
- asserts `current_user = 'galaxy_reader'`;
- asserts one-row access to `galaxy.system_catalog`;
- reports latency through the unchanged `probeResult` shape.

Update together:

- `internal/httpapi/admin_diagnose.go`;
- `internal/httpapi/diagnose_probes.go`;
- `internal/httpapi/kaine.go` dependency wiring;
- `internal/edinbot/controlclient/client.go`;
- `cmd/docker-inspect-sidecar` allowlist and its `ALLOWLIST.md`;
- focused tests.

Requests containing the retired check name `memgraph` return the existing
unknown-check 400. Do not keep an alias that suggests Memgraph is healthy.

### 4.7 Static galaxy exporter

Keep the binary formats and filenames byte-compatible:

- `positions.bin` / `positions.bin.gz`;
- `metadata.json`;
- history artifacts already sourced from PostgreSQL;
- `manifest.json`.

Replace Memgraph flags with `--galaxy-dsn`. Stream systems using a server-side
cursor/read-only transaction ordered by `system_catalog.id64`; join current
system/power values. Do not use OFFSET pagination and do not load the full
galaxy into process memory.

The binary layout stores all positions before all IDs, so the exporter writes
two root-owned temporary spool files (`positions.part`, `ids.part`) while it
streams the cursor, then writes the final header and concatenates the two parts
through the optional gzip writer. It deletes the parts only after the final
artifact is fsynced and atomically renamed. Metadata index arrays may remain in
memory because they contain only systems carrying the corresponding
power/allegiance/state value; the measured 1-million-row projection must still
include their memory cost and hard-stop if full-run RSS projects above 8 GiB.

Add a bounded golden test that compares the relational exporter against a
checked-in legacy-format golden fixture, byte-for-byte except the documented
generation timestamp fields. The test must not require Memgraph after MR9.
Production enabling remains a separate decision; this task only makes the
dormant path safe.

## 5. Execution Waterfall

### MR0 - Freeze inventory and contracts

Deliver:

- this plan approved;
- route inventory above checked against production Go references;
- field map for system detail;
- captured JSON fixtures for:
  - known powerplay system modal factions/stations;
  - system with no faction/station rows;
  - system detail with star, planet, rings, hotspots, and station;
  - 404 detail;
  - viewport with each optional filter;
  - search;
  - stats;
  - survey route with and without explicit start;
- current OpenAPI fragments and frontend types copied to evidence.

Gate:

```bash
rg -n 's\.memgraph|memgraphClient|internal/memgraph|neo4j' \
  internal/httpapi cmd/control-api cmd/galaxy-exporter
```

Every production hit must map to a row in section 2.1. Unknown hits stop MR1.

### MR1 - Extend `galaxystore` contracts

Implement typed request/response contracts and methods for:

- factions;
- stations;
- viewport;
- detail by ID/name;
- prefix search;
- stats;
- survey candidates;
- ordered exporter streaming;
- galaxy-reader diagnostic probe.

Required tests:

- exported-function unit tests;
- empty/not-found/error paths;
- deterministic ordering;
- nullable body `physical` fields;
- empty vs non-empty hotspot arrays;
- carrier exclusion;
- query cancellation and timeout propagation.

Gate:

```bash
GOWORK=off go test -count=1 ./internal/galaxystore
```

### MR2 - Port modal sub-actions

Replace only the `factions` and `stations` switch branches. Delete their
Memgraph nil checks and imports. Preserve history branches unchanged.

Gate:

- handler contract tests compare status and canonical JSON to MR0 fixtures;
- local deployed-data modal makes all four requests without 503;
- history remains raw-backed and is not accidentally redirected.

### MR3 - Port galaxy map APIs

Replace all five handlers in `internal/httpapi/galaxy.go`. The file must not
import `internal/memgraph`. Keep parse and validation behaviour.

Gate:

- existing handler tests are converted to relational fakes/integration
  fixtures, not deleted;
- frontend API service requires no change;
- `EXPLAIN` evidence for bbox, detail-by-name, search, and stats is checked in;
- p95 budget on the new production-shaped database:
  - search/detail/stats warm: at most 500 ms;
  - viewport at default representative bounds: at most 2 seconds;
  - all remain below the 15-second role timeout.

### MR4 - Port survey route

Replace Neo4j session code with the MR1 set-based method. Retain only routing
and response assembly in `survey_route.go`.

Gate:

- no per-anchor SQL;
- exact candidate/station fixture parity;
- duplicate anchor overlap produces one candidate;
- start-system 400 and empty-anchor 200 paths proven;
- representative production-shaped p95 at most 2 seconds for limit 50 and at
  most 5 seconds for limit 500.

### MR5 - Port exporter

Replace its source adapter and CLI flags. Preserve file contracts and atomic
temporary-file rename behaviour.

Gate:

- bounded golden output comparison;
- cancellation leaves no published partial artifact;
- `galaxy_reader` can run it;
- `feed.messages` remains inaccessible;
- 1-million-row measured projection recorded before any full export.

### MR6 - Remove fallback and runtime wiring

In one commit:

- delete Memgraph fallback from powerplay refresh;
- remove Memgraph client initialization/close from `cmd/control-api`;
- remove Memgraph parameter from `httpapi.Run`;
- remove `Server.memgraph`;
- replace diagnostic check and bot request list;
- remove active Memgraph config structs/env parsing;
- remove `MEMGRAPH_ENABLED` from production and local deployed-data templates;
- correct comments and OpenAPI text that still claim current data comes from
  Memgraph.

Gate:

```bash
rg -n 's\.memgraph|memgraphClient|EDIN\.Memgraph|MEMGRAPH_' \
  cmd internal/httpapi internal/tools ansible scripts
```

Result must be empty outside explicitly historical/test-only paths.

### MR7 - MCP and full-backend regression

Run:

```bash
GOWORK=off go test -count=1 ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api ./cmd/galaxy-exporter
GOWORK=off go test -count=1 ./...
```

Run the real-Postgres MCP smoke with `GALAXY_TEST_DSN` as `galaxy_reader`.
The evidence manifest must classify every `ToolName` from
`internal/tools/executor.go` by its authoritative source and record one
successful invocation for every current-galaxy tool, plus one invocation of
each raw-history tool proving the separate history client still works.

Hard assertions:

- `internal/tools` has no Memgraph import or client field;
- `galaxy_query SELECT current_user` returns `galaxy_reader`;
- `galaxy_query SELECT count(*) FROM galaxy.system_catalog` succeeds;
- `galaxy_query SELECT count(*) FROM feed.messages` fails;
- history tools work only when the separate history client is configured;
- no tool schema changed in this task.

### MR8 - Dark deploy and N7 acceptance

Preconditions:

- MR0-MR7 green and committed;
- new host database `ANALYZE` complete;
- maintenance page remains public;
- deployment uses the existing volume-safe Ansible path;
- no Memgraph container is introduced.

Deploy `control-api`, frontend, MCP, and bot dark per N7. Test against the new
host via scoped local routing/staging hostname.

Ordered checks:

1. `/health`;
2. `galaxy-reader` diagnose;
3. MCP reader-hardening assertions;
4. all current-galaxy MCP families;
5. powerplay list and modal history/factions/stations;
6. Kaine system/detail/watch/market/mining surfaces;
7. five `/api/galaxy/*` routes;
8. survey route;
9. exporter bounded smoke;
10. powerplay cache refresh over two refresh intervals.

Any 5xx, contract mismatch, timeout, role mismatch, or Memgraph connection
attempt stops N7. Roll back the application containers only; do not touch
database volumes, listener, or writer.

### MR9 - Remove legacy development/deployment artifacts

Only after MR8 passes:

- remove `internal/memgraph` after moving any still-required pure contract
  fixtures to their owning package;
- remove Neo4j driver dependency if no remaining build target imports it;
- remove local Memgraph Compose/Make targets and stale `.env.dev` entries;
- retire the old `galaxy_exporter` Memgraph role/config;
- remove Memgraph from docker-inspect sidecar allowlists;
- remove legacy Memgraph firewall entries from active inventory only where the
  frozen-old-server contract permits;
- update backend, Atlas, data, and root architecture/readme truth.

Historical evidence and superseded plans are retained and clearly labelled
historical. Volume deletion remains separately authorised and is not part of
MR9.

## 6. Required Evidence Tree

Store compact evidence under:

```text
edin-backend/docs/plans/2026-07-25-memgraph-api-retirement/
  00-inventory.txt
  system-detail-field-map.md
  contract-fixtures/
  explain/
  mcp-source-manifest.md
  local-smoke.md
  prod-dark-smoke.md
  grep-final.txt
```

Do not commit credentials, DSNs, full production responses containing user
data, or bulky query output.

## 7. Completion Gate

This plan is complete only when all are true:

- every section-2.1 API returns its contract from relational PostgreSQL;
- the MCP source manifest accounts for every tool and all MCP gates pass;
- `control-api` cannot initialize or fall back to Memgraph;
- diagnostics no longer request or report Memgraph;
- the exporter cannot connect to Memgraph;
- new production runs without Memgraph configuration or container;
- N7 no longer has an accepted-broken Memgraph surface list;
- live-code grep contains no Memgraph reference outside explicitly retained
  historical evidence during MR8, and none after MR9;
- all docs describe PostgreSQL `galaxy.*` as the sole current-galaxy source.
