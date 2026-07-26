# Memgraph API Retirement Plan

Date: 2026-07-25

Status: **MR0-MR7 COMPLETE LOCALLY 2026-07-25 - MR7A REQUIRED BEFORE MR8**

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

## Amendment record

**2026-07-25 amendment (post adversarial review).** The review returned
BLOCKED. Two changes follow:

1. **Descope (David's decision):** the galaxy visualiser surfaces — the five
   `/api/galaxy/*` routes and `cmd/galaxy-exporter` — are **retired, not
   ported**. The visualiser is behind ComingSoon (`edin-frontend/src/App.jsx`
   routes `/galaxy` and `/galaxy/*` to `ComingSoon`); `/api/galaxy/view` has
   no caller anywhere; `stats`, `search`, and `system/*` are called only by
   the unrouted visualiser page. Porting them was the source of every
   production-scale blocker (unbounded viewport count, full-galaxy aggregates
   under the 15-second role timeout, a browser-infeasible multi-gigabyte
   export). Retirement removes the two largest importers of
   `internal/memgraph` and still achieves the plan's goal. The work needed to
   relaunch the visualiser is recorded in section 8 so it is not lost.
   **Owner decision (David, 2026-07-25): the five routes have no external
   consumers.** This is established fact by decision authority; later
   reviewers do not reopen it.
2. **Review findings folded in:** fixture provenance (MR0), the exact-match
   name resolver and station row-population pins (4.1), survey semantics pins
   (4.5), diagnostics allowlist invariant and deploy-ordering (4.6, MR8), the
   MR6/MR9 gate scope corrections, the Kaine system prompt correction, the
   MR-D3 wording correction, and a new MR9 gate.
3. **2026-07-25 local-chat finding:** Anthropic now rejects the request's
   `compact_20260112` context edit. MR7A (section 9) is a required compatibility
   gate before MR8 and covers both streaming and non-streaming assistant paths.

## 0. Verified Baseline

As of 2026-07-25:

- W5.1-W5.6 are delivered. `internal/tools` no longer imports or receives a
  Memgraph client, and `galaxy_query` is PostgreSQL SQL.
- the live listener, galaxy writer, and relational database run on
  `new-edin-space`; the application stack remains behind maintenance.
- the powerplay current-state list is relational, but its modal's `factions`
  and `stations` calls still return 503 when Memgraph is disabled.
- all five handlers in `internal/httpapi/galaxy.go` still call Memgraph.
  None has a live consumer: the frontend galaxy page is unrouted
  (ComingSoon) and `/api/galaxy/view` has no caller at all.
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
semantics — and delete the Memgraph-bound surfaces that have no consumers
rather than porting them.

The final state is:

1. `control-api` does not construct, receive, probe, or fall back to a
   Memgraph client.
2. Every surviving current-galaxy HTTP read uses `internal/galaxystore`
   through the `galaxy_reader` connection.
3. Every MCP current-galaxy tool remains on `internal/galaxystore`; historical
   tools keep their separate raw-feed connection.
4. The galaxy static-data exporter and the five `/api/galaxy/*` routes are
   deleted. Re-introducing a static export or map API for the visualiser is a
   new design task (section 8), not a latent Memgraph path.
5. production configuration, diagnostics, sidecar allowlists, and deployment
   templates contain no active Memgraph dependency.
6. the new host can pass N7 with `MEMGRAPH_ENABLED` absent, not merely false.

## 2. Scope Boundary

### 2.1 In scope

| Surface | Current Memgraph dependency | Required disposition |
|---|---|---|
| `GET /api/edin/systems/{name}/factions` | `GetFactionsInSystem` | port to `galaxy.system_faction` + `galaxy.faction` |
| `GET /api/edin/systems/{name}/stations` | `GetStationsInSystem` | port to `galaxy.station` + `galaxy.station_stub` per 4.1 |
| `GET /api/galaxy/view` | `GetSystemsInBounds` | retire: delete route, handler, and tests (4.2) |
| `GET /api/galaxy/system/{systemId64}` | `GetSystemDetail` | retire: delete route, handler, and tests (4.2) |
| `GET /api/galaxy/system/name/{systemName}` | `GetSystemDetailByName` | retire: delete route, handler, and tests (4.2) |
| `GET /api/galaxy/search` | `SearchSystemsByPrefix` | retire: delete route, handler, and tests (4.2) |
| `GET /api/galaxy/stats` | `GetGalaxyViewStats` | retire: delete route, handler, and tests (4.2) |
| `GET /api/internal/survey-route` | direct Neo4j sessions | port anchor/candidate lookups to relational spatial SQL |
| `POST /admin/diagnose` check `memgraph` | Memgraph process and Cypher probe | replace with `galaxy-reader` relational probe |
| powerplay cache refresh | relational first, Memgraph fallback | delete fallback; retain stale cache on relational failure |
| `cmd/galaxy-exporter` | `GetAllSystemsMinimal` | retire: delete the command and its build target (4.7) |
| `cmd/control-api` and `httpapi.Server` | constructs and carries Memgraph client | remove wiring after all handlers are relational or deleted |
| `internal/config/prompts/kaine_system_prompt.md` | describes `galaxy_query` as Cypher/Memgraph (lines 21-22, 62) | correct to PostgreSQL SQL in MR6 |
| production Ansible/config | dormant Memgraph switches and labels | remove active configuration and deployment paths |

### 2.2 Explicitly out of scope

These are not Memgraph migrations and MUST NOT be folded into this work:

- `/api/edin/systems/{name}/history` and `expansion-history`; these are
  time-series reads from `feed.powerplay_hourly` through the raw EDDN pool.
- Kaine objectives, chat, users, groups, prompts (other than the factual
  correction named in 2.1), and mining-map ownership; these belong to the
  EDIN application database.
- commander ingest, commander history, Frontier auth, and Copilot sessions.
- Spansh, EDSM, static guide search, operations tools, DayZ, and external APIs.
- any schema or writer-semantic change.
- deletion of old Memgraph volumes or frozen-old-server data.

### 2.2a Named accepted residuals

These Memgraph references survive this plan deliberately. They are listed so
the completion grep has an authoritative exclusion list and so nobody
rediscovers them as gaps:

- **`edin-data/cmd/eddn-listener`** retains an env-gated Memgraph write path
  (`MEMGRAPH_HOST` construction at `main.go:328,366-416`) and `edin-data`
  keeps the Neo4j driver for it. The deployed listener template does not set
  the variable. Removing this path touches the live listener and is excluded
  by the writer-change boundary above; it is tracked as a follow-up in the
  delivery plan, not here. Goal 5 is therefore scoped to `edin-backend`,
  `atlas`, and deployment templates — not the listener binary's dormant
  branch.
- **Atlas firewall deny rules** for 7687/7444 (`firewall_blocked_ports`) are
  defence-in-depth and are retained. Only retired *allow* entries are removed
  in MR9, and only where the frozen-old-server contract permits.
- **Frozen old-server inventory** (`atlas/ansible/host_vars/db.edin.space.yml`)
  is untouched.

### 2.3 MCP disposition

MCP is a verification task, not an implementation rewrite. W5.5 and W5.6
already removed Memgraph from `internal/tools`.

| MCP family | Authoritative source after this plan |
|---|---|
| current galaxy: system, station, carrier, body, signal, market, faction, power, expansion, surface-site, stats, schema, `system_profile` | `galaxy.*` through `galaxystore` |
| mining tools (`galaxy_plasmium_buyers`, `galaxy_ltd_buyers`, `galaxy_expansion_targets`) | dual source: mining maps from the EDIN application database (kaine store) + galaxy state through `galaxystore` |
| `galaxy_query` | parser-restricted SQL under `galaxy_reader` |
| `galaxy_history`, `galaxy_powerplay_cycle` | raw EDDN history connection |
| commander tools | commander repository |
| guides | checked-in reference material |
| Spansh/route tools | external clients |
| operations tools | operations manager |
| `describe_tool` and other meta-tools | static tool definitions in `internal/tools` |

No MCP tool may receive a Memgraph client or import `internal/memgraph`.

## 3. Non-Negotiable Contracts

### MR-D1: One current-state store

`internal/galaxystore` owns all production reads from `galaxy.*`. HTTP handlers,
MCP tools, and background refreshes must not embed parallel SQL
implementations when the same projection is shared.

### MR-D2: Existing wire contracts stay stable — with two enumerated exceptions

The frontend does not change to accommodate this migration. For the surviving
routes (modal sub-actions, survey), existing query parameters, defaults, caps,
HTTP status classes, JSON names, omitted fields, array ordering, and cache
headers remain stable.

Enumerated exceptions, approved 2026-07-25:

1. The five `/api/galaxy/*` routes are deleted (they have no consumers); a
   request to them returns the router's standard 404.
2. Where the graph implementation returned nondeterministic collection order,
   the relational implementation pins deterministic domain order and records
   it in tests:
   - factions: influence descending, faction name ascending;
   - stations: distance from arrival ascending (null distance coerced to `0`,
     which therefore sorts first — matching the legacy handler), then name.

Empty-collection encoding is pinned **per surface from the MR0 fixtures**, not
by a blanket rule: the modal sub-actions emit `[]` (legacy behaviour); survey
emission matches its legacy fixtures exactly. Do not generalise an
"always `[]`" or "always `null`" rule to any surface.

Zero timestamps: legacy `time.Time` fields tagged `omitempty` are **never
omitted** by `encoding/json` — missing graph timestamps shipped as
`"0001-01-01T00:00:00Z"`. Ported DTOs must reproduce this: map relational NULL
to the zero `time.Time` and emit it. Do not switch to `*time.Time` or
`omitzero`.

### MR-D3: Read roles remain separated

`GALAXY_READER_DSN` is the only connection used for `galaxy.*`. It must connect
as `galaxy_reader`, with no `feed.*` privilege.

Raw history retains its own **separate** raw-feed connection. That connection
currently authenticates as `eddn_admin`; moving it to a dedicated reader role
is desirable but is tracked separately and is not part of this plan. This plan
must not grant `feed` to `galaxy_reader` to make history endpoints convenient,
and MR8 evidence must not claim the history connection is read-only.

### MR-D4: No fallback to Memgraph

After a handler is ported, its Memgraph fallback is deleted in the same commit.
A relational query error is observable and preserves existing stale cache where
that cache exists; it never silently switches databases.

### MR-D5: Query plans are gates

Every new query is run against the production-shaped database (defined in
MR-D6) with `EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS)` after `ANALYZE`.

API transactions also retain the reader safety floor:

- role `statement_timeout`: 15 seconds. Note this is a per-**statement**
  limit and a session-overridable role *default*; it is treated as a floor by
  convention. No code in this plan may raise it.
- `galaxy_query`: 2 seconds (existing `SET LOCAL`).
- read-only transaction where multiple statements form one projection.
- no sequential scan of `galaxy.system_catalog` for bounded spatial lookups.
- no per-row station or faction query; collections are fetched set-wise.

### MR-D6: No live deployment until the complete set is ready

MR0-MR7 build and test locally. MR8 is one dark `control-api` deployment with
all route ports and retirements, MCP verification, diagnostics changes, and
Memgraph wiring removal together. There is no mixed production mode.

**Production-shaped database, defined:** the new host's `eddn_raw` database
accessed **read-only as `galaxy_reader`** over the existing operator SSH path.
Running `EXPLAIN` and read-only measurement there is not a deployment and is
permitted from MR1 onward. Where a query can be exercised meaningfully at
local scale first, do that first; the production-shaped run is the gate.

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

**Name resolution is exact and case-sensitive**, matching the graph pattern
`{name: $name}`. The existing `galaxystore` resolver
(`internal/galaxystore/system.go` `GetSystemFullByName`) resolves via
`lower(c.name) = lower($1)` and **must not be reused** — implement a new
exact-match resolver on `system_catalog.name = $1`. Wrong-case lookups return
the empty result, and MR0 captures a wrong-case fixture proving it.
Unknown systems return an empty slice to preserve the current sub-action
contract. Do not call `GetSystemFull` and load unrelated slices.

**Station row population is pinned**, because `galaxy.station` and the legacy
graph do not contain the same row set:

- exclude construction depots: `WHERE kind = 'station'` (`galaxy.station`
  also holds `space_depot`/`planetary_depot` rows the graph never surfaced);
- include FSS station stubs: legacy FSS-discovered stubs were graph `Station`
  nodes and appeared in this response with `market_id` `0`, an empty
  `landing_pads` object, no services, null distance, and FSS-vocabulary
  types. Relationally they live only in `galaxy.station_stub`. UNION them in,
  deduplicated by station name with the full `galaxy.station` row winning;
- carriers: the handler retains its exact current post-query filter —
  case-insensitive `Fleetcarrier` is removed. Do not add another filter.
  (Carriers do not enter `galaxy.station` relationally, so the filter is
  expected to be a no-op; keep it anyway for contract fidelity and record
  that expectation in the MR1 tests.)

The handler maps relational rows into a dedicated wire DTO matching the MR0
legacy fixture. In particular, the current graph sub-action sets
`system_name` but leaves `system_id64` omitted; directly encoding the broader
`galaxystore.StationData` would add a field and is forbidden. Faction rows
likewise echo the requested `system_name`. Zero-timestamp emission follows
MR-D2.

Before coding, add
`docs/plans/2026-07-25-memgraph-api-retirement/modal-field-map.md`
enumerating every field of the legacy modal faction and station wire shapes,
its exact relational expression (including the stub-row expressions), null
rule, and JSON omission rule. An unmapped field is a hard stop, not
permission to emit zero.

The HTTP handler keeps the existing wrappers:

```json
{"system_name":"Wolf 1060","factions":[]}
```

```json
{"system_name":"Wolf 1060","stations":[]}
```

### 4.2 Galaxy visualiser routes — RETIRED (amended 2026-07-25)

The former sections 4.2 (viewport), 4.3 (system detail), and 4.4
(search/stats) directed ports of the five `/api/galaxy/*` handlers. They are
**withdrawn**. Instead:

- delete the five route registrations and handlers
  (`internal/httpapi/galaxy.go` in its entirety) and their tests;
- delete the graph-era viewport/detail/search/stats request/response types
  with them; move nothing into `galaxystore`;
- the frontend does not change: the galaxy page is already unrouted
  (ComingSoon) and its `services/api.js` becomes dead code awaiting the
  visualiser relaunch (section 8);
- requests to the deleted paths receive the router's standard 404. No stub
  handler, no redirect, no alias.

Rationale: no consumers exist — internally verified (unrouted ComingSoon
page; `view` called by nothing) and externally **confirmed by David as an
owner decision on 2026-07-25**; porting them carried the plan's only
production-scale risks (unbounded viewport count, full-galaxy aggregates
against the 15-second reader timeout); and deleting them removes the largest
`internal/memgraph` importer.

### 4.3 Galaxy system detail — withdrawn, see 4.2

### 4.4 Search and stats — withdrawn, see 4.2

### 4.5 Survey route

Keep route/auth/query parameters/response unchanged. Replace direct Neo4j
sessions with one `galaxystore` method that accepts:

- mining-map system names;
- optional start system.

The method returns the complete candidate set because the HTTP response must
report `total_candidates` before applying its requested limit. The unchanged
handler applies the 50-default/500-maximum limit after stale-first ordering.

The method performs:

1. resolve all map anchors in one query;
2. retain anchors only when current powerplay state is `Fortified` (20 ly) or
   `Stronghold` (30 ly);
3. find all candidate systems with population greater than zero and at least
   one large-pad station of type `Coriolis`, `Orbis`, `Ocellus`, `Dodec`, or
   `Asteroidbase`;
4. use the `system_catalog` cube index for each radius via a lateral/values
   anchor relation — the pinned construct is
   `cube(ARRAY[c.x,c.y,c.z]) <@ cube_enlarge(cube(ARRAY[a.x,a.y,a.z]::float8[]), a.radius, 3)`
   inside a `CROSS JOIN LATERAL` — and apply exact Euclidean distance after
   the cube prefilter;
5. deduplicate candidates **by system name**, matching the legacy handler's
   seen-map (`survey_route.go` keys its dedup on name, not id64);
6. aggregate and order qualifying stations by distance then name, with null
   `distance_ls` coerced to `0` (which sorts first — legacy behaviour);
7. derive `last_update` as
   `GREATEST(system.last_event_time, system.last_faction_update,
   system_power.last_event_time)`, ignoring nulls through the existing
   `-infinity`/`NULLIF` expression. **Declared deviation:** legacy read the
   single graph property `last_eddn_update`; the relational expression is a
   deliberate semantic remap, so staleness ordering (and therefore stale-first
   candidate selection) may differ from a legacy capture. MR4 parity is
   defined against the relational expression, not against legacy output
   values;
8. preserve the current stale-first selection and Go nearest-neighbour route.

Behavioural pins carried over from the legacy handler:

- an unknown explicit `start` remains HTTP 400 — and so does a database
  *error* during the start lookup (legacy returns 400 for both);
- missing mining-map anchors remain a successful empty response;
- `mining_maps_used` remains `len(mapSystems)` on the no-anchor early return
  and `len(anchors)` in the full response — preserve the discrepancy, do not
  "fix" it.

The SQL must be one set-based candidate query, not one query per mining-map
anchor.

### 4.6 Diagnostics

Replace the request check name `memgraph` with `galaxy-reader`.

`galaxy-reader`:

- inspects container `eddn-timescaledb` (verified: `galaxy.*` lives in the
  `eddn_raw` database inside that container; the container is already present
  in the sidecar allowlist, so no sidecar *addition* is needed);
- runs a bounded query through the `GALAXY_READER_DSN` pool;
- asserts `current_user = 'galaxy_reader'`;
- asserts one-row access to `galaxy.system_catalog`;
- reports latency through the unchanged `probeResult` shape.

Expected dev-mode behaviour: with `EDDN_RAW_DB_ENABLED=true` and no
`GALAXY_READER_DSN`, the galaxystore runs on the `eddn_admin` pool and this
probe **fails its `current_user` assertion by design**. That is correct
fail-loud behaviour, not a defect; record it in the probe's test.

Update together in MR6:

- `internal/httpapi/admin_diagnose.go` (including its allowlist comment);
- `internal/httpapi/diagnose_probes.go`;
- `internal/httpapi/kaine.go` dependency wiring (a galaxystore-backed probe
  replaces `memgraphProber`/`nilMemgraphProber`);
- `internal/edinbot/controlclient/client.go` (hardcoded checks list);
- `cmd/docker-inspect-sidecar/main.go` allowlist: **remove** the `memgraph`
  entry (this is the only sidecar change, and it happens here, once — MR9
  merely verifies it);
- `cmd/docker-inspect-sidecar/ALLOWLIST.md`: rewrite. The documented
  "lock-step" cross-check does not exist — both tests hardcode independent
  copies of the list. Restate the invariant as: *the set of `container`
  values in `allowedDiagnoseChecks` is a subset of the sidecar's
  `allowedContainers()`*, and assert that mechanically in the tests (compare
  container values, not check names — after this change the check-name set
  and container-name set are intentionally unequal);
- focused tests, including the two hardcoded want-lists
  (`cmd/docker-inspect-sidecar/main_test.go`,
  `internal/httpapi/admin_diagnose_test.go`).

Requests containing the retired check name `memgraph` return the existing
unknown-check 400. Do not keep an alias that suggests Memgraph is healthy.
Note that check validation is atomic (any unknown name 400s the whole
request), so bot and control-api must ship together — see MR8.

### 4.7 Static galaxy exporter — RETIRED (amended 2026-07-25)

The former section directed a port of `cmd/galaxy-exporter` to relational
streaming. It is **withdrawn**. Instead:

- delete `cmd/galaxy-exporter` and its Makefile build target;
- delete the empty Ansible role skeleton
  `edin-backend/ansible/roles/galaxy_exporter/` (already gutted; nothing
  deploys it);
- do not touch `/galaxy-data/*` serving in Caddy or any artifact already on
  disk; the visualiser relaunch (section 8) owns that surface's future.

Rationale: the exporter is dormant, undeployed, and its faithful port at
relational scale (126M+ catalog rows vs the legacy ~633k-system graph) would
produce a browser-infeasible multi-gigabyte artifact. The export pipeline
must be **redesigned around a pinned membership predicate**, which is
visualiser product work, not Memgraph retirement work.

## 5. Execution Waterfall

### MR0 - Freeze inventory and contracts

Deliver:

- this plan approved;
- route inventory above checked against production Go references;
- `modal-field-map.md` per 4.1;
- captured JSON fixtures. **Provenance is pinned**: fixtures come in two
  classes, and every parity gate names which class it compares against:
  - **shape fixtures**, derived from the checked-in legacy handlers and their
    concrete response DTOs, then enforced by canonical-JSON handler tests:
    key sets, omission behaviour, wrapper shapes, ordering rules, status
    codes. Values in shape fixtures are not compared; a local Memgraph dataset
    is not treated as an independent value oracle;
  - **value fixtures**, captured from the relational implementation against a
    seeded relational test database whose expected values are known
    independently (hand-computed from the seed data, not derived by running
    the implementation);
- fixture set:
  - known powerplay system modal factions/stations (including a system with a
    station stub and a system whose `galaxy.station` row set includes a
    depot, proving inclusion/exclusion per 4.1);
  - system with no faction/station rows;
  - **wrong-case system name** for both sub-actions (must return the empty
    contract);
  - survey route with and without explicit start, including a
    duplicate-anchor overlap case;
- current OpenAPI fragments and frontend types copied to evidence;
- cross-document corrections landed so no live document contradicts this
  plan:
  - `edin-backend/docs/plans/2026-07-07-galaxy-relational-read-port.md`
    W5.6 evidence note: replace "still tracked for W8 retirement/porting"
    with a pointer to this plan;
  - `edin-data/docs/plans/galaxy-relational-delivery-plan.md`: strike the
    residual "W8 … exporter retire-or-port" wording and annotate section 8.1
    (compose removal) as superseded — the only Memgraph compose is on the
    frozen old server;
  - note for MR6: the local deployed-data launcher that sets
    `MEMGRAPH_ENABLED=false` is
    `edin-frontend/scripts/dev-deployed-data.sh:121` — a different repo;
    it is an enumerated MR6 deliverable so it is not lost to the gate grep.

Gate:

```bash
rg -n 's\.memgraph|memgraphClient|internal/memgraph|neo4j' \
  internal/httpapi cmd/control-api cmd/galaxy-exporter
```

Every production hit must map to a row in section 2.1. Unknown hits stop MR1.

### MR1 - Extend `galaxystore` contracts

Implement typed request/response contracts and methods for:

- factions;
- stations (including stub UNION and depot exclusion per 4.1);
- survey candidates;
- galaxy-reader diagnostic probe.

Also in MR1 — fix the live LIKE-wildcard defect in
`internal/galaxystore/search.go:50` (reachable through MCP search today, so
it does not wait for the visualiser register): escape `\`, `%`, and `_` in
the user-supplied prefix before building the pattern
(e.g. `strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")`), and make
the escape character explicit in the SQL:
`lower(c.name) LIKE lower($1) || '%' ESCAPE '\'`. This preserves
literal-prefix semantics and changes no wire contract — it makes the query
mean what its callers already assume.

Required tests:

- exported-function unit tests;
- empty/not-found/error paths;
- deterministic ordering, including null-distance-sorts-first;
- wildcard-escaping tests for search: inputs containing `%`, `_`, and `\`
  match literally, and `EXPLAIN` evidence (parameterised form, checked into
  the evidence tree) that an escaped leading-`%` input still drives
  `idx_catalog_name_prefix` rather than a sequential scan;
- wrong-case name returns empty;
- stub inclusion and depot exclusion;
- carrier-filter no-op expectation recorded;
- zero-timestamp emission;
- query cancellation and timeout propagation.

Note MR-D5: the repeatable-read read-only transaction pattern is new code —
`galaxystore` today exposes only default-isolation `BeginReadOnly`; add the
explicit `pgx.TxOptions{IsoLevel: RepeatableRead, AccessMode: ReadOnly}`
variant where a projection spans multiple statements.

Gate:

```bash
GOWORK=off go test -count=1 ./internal/galaxystore
```

### MR2 - Port modal sub-actions

Replace only the `factions` and `stations` switch branches. Delete their
Memgraph nil checks and imports. Preserve history branches unchanged.

Gate:

- handler contract tests compare status and canonical JSON shape to MR0
  shape fixtures, and values to MR0 value fixtures against the seeded test
  database;
- wrong-case fixture passes;
- local deployed-data modal makes all four requests without 503;
- history remains raw-backed and is not accidentally redirected.

### MR3 - Retire galaxy map APIs

Delete the five `/api/galaxy/*` route registrations, all handlers in
`internal/httpapi/galaxy.go`, the file itself, its tests, and the graph-era
request/response types they used.

Gate:

- `GOWORK=off go build ./...` and `go test ./...` pass;
- the five paths return 404 in a local smoke;
- no frontend change (the galaxy page remains unrouted; its API service is
  dead code and is left in place for section 8);
- `internal/httpapi` no longer imports `internal/memgraph` except via
  `server.go` wiring that MR6 removes.

### MR4 - Port survey route

Replace Neo4j session code with the MR1 set-based method. Retain only routing
and response assembly in `survey_route.go`.

Gate:

- no per-anchor SQL;
- shape parity against MR0 shape fixtures; value parity against the seeded
  relational value fixtures (the `last_update` remap in 4.5 makes value
  comparison against legacy captures invalid by design);
- duplicate anchor overlap produces one candidate (deduplicated by name);
- start-system 400 (unknown and DB-error), empty-anchor 200, and
  `mining_maps_used` discrepancy paths proven;
- representative production-shaped p95 at most 2 seconds for the complete
  candidate projection, with `EXPLAIN` evidence showing `idx_catalog_loc`
  driving the lateral probes. This single gate dominates both limit 50 and
  limit 500 because the projection is deliberately identical for both.

### MR5 - Retire static galaxy exporter

Delete `cmd/galaxy-exporter`, its Makefile build target, and the empty
`ansible/roles/galaxy_exporter/` skeleton. Do not touch `/galaxy-data/*`
serving or artifacts on disk.

Gate:

- `GOWORK=off go build ./...` and `go test ./...` pass;
- `rg -n 'galaxy-exporter|GetAllSystemsMinimal' cmd internal Makefile
  ansible` returns only historical/docs hits;
- root-repo docs that referenced exporter deploy targets are corrected
  (`edin-space/CLAUDE.md` `make deploy-backend-exporter`).

### MR6 - Remove fallback and runtime wiring

In one commit:

- delete Memgraph fallback from powerplay refresh;
- remove Memgraph client initialization/close from `cmd/control-api`;
- remove Memgraph parameter from `httpapi.Run`;
- remove `Server.memgraph`;
- replace diagnostic check per 4.6 (including the sidecar allowlist edit and
  ALLOWLIST.md rewrite) and the bot request list;
- remove active Memgraph config structs/env parsing
  (`internal/config/config.go` MemgraphConfig and `MEMGRAPH_ENABLED`
  parsing);
- remove `MEMGRAPH_ENABLED` from the production template
  (`ansible/roles/control_api/templates/control-api.env.j2`) and the local
  deployed-data launcher
  (`edin-frontend/scripts/dev-deployed-data.sh`);
- correct `internal/config/prompts/kaine_system_prompt.md` lines 21-22 and
  62: `galaxy_query` is PostgreSQL SQL, not Cypher; remove Cypher-specific
  guidance;
- correct comments that still claim current data comes from Memgraph,
  including `internal/tools/scopes.go:30`, `internal/authz/authz.go:24`, and
  OpenAPI text;
- dispose of the build-tagged Memgraph integration tests in the same commit —
  both reference the `Server.memgraph` field this MR deletes, and their build
  tags hide them from the ordinary `go test ./...` gate:
  - `internal/httpapi/kaine_integration_test.go` (tag `integration`):
    **delete**. It already fails to compile against the current Anthropic SDK
    and `memgraph.Client` (documented in its sibling's header) and targets
    the frozen old server at 10.8.0.3;
  - `internal/httpapi/kaine_search_integration_test.go` (tag
    `integration_search`): **rewrite against seeded relational PostgreSQL**.
    It guards a live Kaine search surface; the rewrite must not use
    `testutil.StartTestMemgraph` (that harness is deleted in MR9). The
    rewritten test connects via `GALAXY_TEST_DSN` (the same variable the MR7
    harness uses) and **fails, not skips, when it is unset** — a tagged run
    must never pass by skipping.

Gate — repo-wide, not path-scoped (the previous gate could not see
`internal/config` or the repo root):

```bash
rg -n 's\.memgraph|memgraphClient|EDIN\.Memgraph|MEMGRAPH_|Cypher' \
  --glob '!docs/**' --glob '!deprecated/**' \
  --glob '!internal/memgraph/**' --glob '!internal/testutil/**' \
  --glob '!docker-compose.local.yml' --glob '!docker/memgraph/**' \
  --glob '!.env.dev' .
```

run from the `edin-backend` root, plus the same pattern over
`edin-frontend/scripts/dev-deployed-data.sh`. Result must be empty. The
excluded globs are exactly the MR9 removal list plus retained historical
docs; nothing else may be excluded.

Tagged compile/test gates (build tags escape the ordinary gate):

```bash
GOWORK=off go test -tags=integration -run '^$' -count=1 ./internal/httpapi
GOWORK=off go test -tags=integration_search -count=1 ./internal/httpapi
```

The first proves the `integration` tag no longer drags in deleted Memgraph
symbols; the second runs the rewritten relational search tests.

### MR7 - MCP and full-backend regression

Run:

```bash
GOWORK=off go test -count=1 ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api
GOWORK=off go test -count=1 ./...
```

Run the real-Postgres MCP smoke. The harness requires **three** connections,
named explicitly because no single DSN can construct every tool:

- `GALAXY_TEST_DSN` as `galaxy_reader` (galaxystore-backed tools and
  `galaxy_query`);
- an EDIN application database test DSN (kaine store — required by the three
  mining tools, whose mining maps live there);
- the raw EDDN history DSN (the two history tools).

The evidence manifest must classify every `ToolName` from
`internal/tools/executor.go` by its authoritative source per the 2.3 table
(including `describe_tool`, `system_profile`, and the dual-source mining
tools) and record one successful invocation for **each individual
current-galaxy tool** — all of them, not representative families — plus one
invocation of each raw-history tool proving the separate history client
still works.

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

Deploy `control-api`, frontend, MCP, and bot dark per N7. **control-api and
the bot deploy in the same step**: diagnose check validation is atomic, so a
version skew between them degrades the entire diagnose report (not just one
check) until both are restarted. That degradation window is expected and
bounded to this step.

Test against the new host via scoped local routing/staging hostname.

Ordered checks:

1. `/health`;
2. `galaxy-reader` diagnose;
3. MCP reader-hardening assertions, plus a one-row
   `has_table_privilege('galaxy_reader', t, 'SELECT')` sweep over
   `galaxy.*` proving no ungranted table (default privileges are keyed to
   `eddn_admin` only; a table created by another role would silently break a
   projection);
4. all current-galaxy MCP tools per the MR7 manifest;
5. powerplay list and modal history/factions/stations;
6. Kaine system/detail/watch/market/mining surfaces;
7. the five retired `/api/galaxy/*` paths return 404;
8. survey route;
9. powerplay cache refresh over two refresh intervals.

Any 5xx, contract mismatch, timeout, role mismatch, or Memgraph connection
attempt stops N7. Roll back the application containers only; do not touch
database volumes, listener, or writer.

### MR9 - Remove legacy development/deployment artifacts

Only after MR8 passes. Enumerated deliverables (the previous list named
artifacts that have since drifted; this list is verified against the tree as
of 2026-07-25):

- remove `internal/memgraph` after moving any still-required pure contract
  fixtures to their owning package;
- remove `internal/testutil/memgraph.go`, `memgraph_fixtures.go`,
  `memgraph_test.go` (the testcontainers harness — it imports the Neo4j
  driver and gates its removal) and the stale `test-integration` comment in
  the backend `Makefile`;
- remove the Neo4j driver from `go.mod`/`go.sum` (after the two removals
  above, no build target imports it);
- remove the local Memgraph development stack: `docker-compose.local.yml`
  Memgraph service, `docker/memgraph/` (`init.cypher`, `render-init.sh`,
  `README.md`), and the stale `.env.dev` Memgraph entries. (The
  `make memgraph-local*` targets referenced by the compose header no longer
  exist; nothing to remove there);
- verify the sidecar allowlist carries no `memgraph` entry (removed in MR6);
- remove legacy Memgraph firewall **allow** entries from active inventory
  only where the frozen-old-server contract permits; the 7687/7444 **deny**
  entries in `firewall_blocked_ports` are defence-in-depth and stay;
- update backend, Atlas, data, and root architecture/readme truth, including
  the `eddn-init-schema.sql` Memgraph-rebuild comments in `edin-data` if
  touched in the same pass.

Historical evidence and superseded plans are retained and clearly labelled
historical. Volume deletion remains separately authorised and is not part of
MR9.

Gate (new — the previous draft shipped MR9 untested):

```bash
GOWORK=off go build ./...
GOWORK=off go test -count=1 ./...
GOWORK=off go test -tags=integration -run '^$' -count=1 ./internal/httpapi
GOWORK=off go test -tags=integration_search -count=1 ./internal/httpapi
rg -n 'memgraph|Memgraph|MEMGRAPH|neo4j|bolt://' --glob '!docs/**' --glob '!deprecated/**' .
```

The grep must return nothing outside labelled historical documents. The MR9
build is the binary production will next run: deploy it dark through the
same volume-safe path and re-run MR8 ordered checks 1, 2, 5, and 8 before
any public cutover. MR9 is not complete on a green local build alone.

## 6. Required Evidence Tree

Store compact evidence under:

```text
edin-backend/docs/plans/2026-07-25-memgraph-api-retirement/
  00-inventory.txt
  modal-field-map.md
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

- every surviving section-2.1 API returns its contract from relational
  PostgreSQL; every retired route returns 404 and its code is deleted;
- the MCP source manifest accounts for every tool and all MCP gates pass;
- `control-api` cannot initialize or fall back to Memgraph;
- diagnostics no longer request or report Memgraph;
- `cmd/galaxy-exporter` no longer exists;
- new production runs without Memgraph configuration or container;
- N7 no longer has an accepted-broken Memgraph surface list;
- live-code grep contains no Memgraph reference outside the named accepted
  residuals (2.2a) and explicitly retained historical evidence during MR8,
  and none in `edin-backend` after MR9;
- MR7A's Anthropic compatibility tests and authenticated local web/streaming
  chat smoke checks pass against the configured model;
- all docs describe PostgreSQL `galaxy.*` as the sole current-galaxy source.

## 8. Deferred: Galaxy Visualiser Relaunch Register

Not part of this plan. Recorded so the descope does not silently lose the
work. Un-ComingSoon-ing `/galaxy` requires a new plan covering:

1. **Export membership**: a pinned predicate defining which systems the
   static artifact contains (the legacy graph held ~633k systems; the
   relational catalog holds 126M+; the browser loader design assumes the
   whole artifact fits in memory — ~20 bytes/system). Populated ∪
   powerplay-bearing is the candidate baseline.
2. **Export pipeline redesign**: DECLARE/FETCH cursor batching under the
   15-second per-statement timeout, spool-file design, deterministic golden
   testing (fixed clock; compare decompressed bytes), cross-file generation
   atomicity, Caddy `precompressed` serving, and a non-root service user.
3. **Stats**: a `galaxy.stats_snapshot` table (or matview) refreshed by
   data-layer tooling under a maintenance role, read by `galaxy_reader` —
   or a sanctioned `SET LOCAL statement_timeout` for an in-process refresh.
4. **Search**: the visualiser's search endpoint contract (route deleted in
   this plan). The underlying LIKE-wildcard escaping defect in
   `internal/galaxystore/search.go:50` is **fixed in MR1 of this plan** — it
   is live via MCP search today and does not wait for the visualiser.
5. **System detail**: the full field-map exercise (body `physical` typed
   extraction, `BodyInfo.ID64` synthesis `SystemAddress + BodyID<<55`,
   hotspot display-name reverse map, ring/stub row-population rules) — the
   review's findings are recorded in the review response document alongside
   this plan.
6. **Viewport API**: decide whether a server-side viewport query is needed at
   all (the binary loader superseded it); if yes, a capped-count contract.

## 9. MR7A - Anthropic API Compatibility (Required Before MR8)

Added 2026-07-25 after the first authenticated local Kaine chat request exposed
an upstream API contract change. This is part of the MR7 application regression
surface and MUST complete before MR8. It does not reopen the relational-read or
Memgraph dispositions in this plan.

### 9.1 Observed failure and scope

The local request reached Anthropic and received HTTP 400
`invalid_request_error`:

```text
context_management.edits.0: Input tag 'compact_20260112' found using 'type'
does not match any of the expected tags: 'clear_thinking_20251015',
'clear_tool_uses_20250919'
```

This is authenticated schema rejection, not an absent or invalid API key.

Both assistant execution paths construct the rejected edit:

- `internal/assistant/runner.go` (`RunWithProgress`, used by Kaine web chat);
- `internal/assistant/runner_streaming.go` (`RunWithStreaming`, used by the
  streaming/client path).

Both currently send the `compact-2026-01-12` and
`context-management-2025-06-27` beta headers and include
`compact_20260112` plus `clear_tool_uses_20250919` in
`context_management.edits`. The repository pins
`github.com/anthropics/anthropic-sdk-go v1.22.1`; the current upstream SDK is
newer, so generated SDK types are not proof that the live API still accepts a
request.

At the time of the original failure, the configured model was
`claude-opus-4-6`. The current compaction documentation explicitly lists that
model as supported. Live one-token probes on 2026-07-25 proved all three exact
request shapes against `claude-opus-4-6`:

| Request | Result |
|---|---|
| `compact-2026-01-12` + `compact_20260112` only | accepted |
| `context-management-2025-06-27` + `clear_tool_uses_20250919` only | accepted |
| both beta headers + both edits | accepted |

The observed 400 is therefore not evidence that Opus 4.6 or server-side
compaction is unsupported. Treat it as a request-path/runtime compatibility
failure until the rebuilt application sends and records the same accepted
wire shape.

David changed the configured model to `claude-sonnet-5` on 2026-07-25. Sonnet
5 is also explicitly listed as supporting server-side compaction. The MR7A
live gate applies to the new configured model; the Opus results above remain
historical evidence and do not substitute for the Sonnet 5 gate.

The runtime-path cause was subsequently identified: quick-dev inherited
`ANTHROPIC_BASE_URL=http://127.0.0.1:8787` from its parent process and sent the
failing request to that local compatibility proxy, while the successful probes
went directly to Anthropic. Development configuration now pins
`ANTHROPIC_BASE_URL=https://api.anthropic.com`. The application smoke gate must
record the effective host so this class of inherited-environment drift cannot
recur silently.

The ordinary non-beta `internal/anthropic.Client.Complete` path does not send
context management and is outside this specific failure.

The code review against
`supporting-docs/anthropic/compaction-api-readme.md` found two independent
contract violations that must be fixed even after the 400 disappears:

1. `extractBetaCompactionBlocks` and `sanitizeBetaAssistantMessage` convert a
   typed `compaction` block into an ordinary text block. The API contract
   requires the compaction block to be round-tripped as a compaction block;
   only then does the API ignore content before it.
2. `RunWithStreaming` does not capture `compaction` block start/delta events or
   a compaction stop reason. The streaming/client path therefore cannot
   preserve or continue from compaction.

Session persistence is currently `llm.Message{Role, Content}` and cannot store
a typed compaction block. MR7A must either extend the persisted message
contract to retain typed Anthropic content safely or introduce an explicit,
tested compacted-session state. Re-emitting the summary as ordinary assistant
text while retaining the old history is forbidden.

### 9.2 Pinned resolution procedure

1. Record the current configured model, SDK version, exact beta headers, edit
   types, and redacted API error under
   `docs/plans/2026-07-25-memgraph-api-retirement/anthropic-compat.md`.
   Never record the API key or complete user conversation.
2. Check the current official Anthropic API documentation and current Go SDK
   release. Upgrade the SDK in an isolated change if required to represent the
   current API contract; run the full build/test gate before changing request
   behavior.
3. Add one shared request-builder helper used by BOTH `RunWithProgress` and
   `RunWithStreaming`. Duplicated beta headers, thresholds, or edit
   construction are forbidden after MR7A.
4. Retain both currently verified strategies:
   - `compact_20260112` under `compact-2026-01-12`;
   - `clear_tool_uses_20250919` under
     `context-management-2025-06-27`.
   Do not send `clear_thinking_20251015` unless extended thinking is enabled
   and a fixture proves thinking-block round trips.
5. Capture the effective outbound URL, model, beta values, and edit type names
   in a redacted request-recorder test. Assert the rebuilt local application
   matches the accepted live probe. Do not log headers generally because the
   same request carries the API key.
6. Preserve every compaction response block as its typed SDK parameter
   (`ToParam` or the current SDK equivalent), including across persisted user
   turns. Both streaming and non-streaming paths must implement the documented
   continuation behavior. The compaction instructions must explicitly say not
   to call tools while summarizing, because tools are present on these
   requests.
7. Account for `usage.iterations` when recording usage or cost. Top-level usage
   excludes compaction iterations.
8. Do not retry HTTP 400 schema errors. Return a sanitized user-facing message
   while logging the Anthropic request ID, status, and structured error without
   credentials or conversation content.

### 9.3 Tests and live gate

Required automated tests:

- the shared builder emits only the pinned, currently supported beta headers
  and edit tags;
- streaming and non-streaming paths use byte-equivalent context-management
  configuration;
- rejected/deprecated edit tags cannot reappear as string constants outside
  explicitly historical documentation;
- an Anthropic 400 is surfaced once and is not retried;
- existing tool filtering, tool-result pairing, prompt caching, and streaming
  callbacks remain unchanged;
- a multi-turn fixture proves the complete typed compaction block is appended,
  persisted, restored, and round-tripped rather than reduced to text;
- a streaming fixture proves `compaction` start/delta/stop handling and
  continuation;
- usage accounting includes compaction iterations without double-counting the
  top-level message usage.

Gate:

```bash
GOWORK=off go build ./...
GOWORK=off go test -count=1 ./internal/assistant ./internal/anthropic ./internal/httpapi ./internal/mcp
GOWORK=off go test -count=1 ./...
rg -n 'compact_20260112|compact-2026-01-12' \
  --glob '!docs/**' --glob '!deprecated/**' .
```

Every grep hit must be in the shared builder or its focused tests.

Then exercise locally with the real configured model and local Authentik:

1. new Kaine web-chat session sends a plain text turn;
2. a second turn proves conversation continuity;
3. one tool-using turn completes its tool call and response;
4. the streaming/client path completes a text turn and a tool-using turn;
5. backend logs contain no 4xx response, deprecated edit tag, credential, or
   full conversation body.

Store redacted results in `anthropic-compat.md`. Any failure stops MR8.
