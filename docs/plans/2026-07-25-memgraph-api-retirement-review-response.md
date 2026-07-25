# Review Response — Memgraph API Retirement Plan

Date: 2026-07-25
Responds to: adversarial review of `2026-07-25-memgraph-api-retirement-plan.md`
at commit `3525c1d` (verdict: **BLOCKED**)
Plan amendment: applied 2026-07-25 in the working tree (see the plan's
Amendment record). Decision authority: David.

## 1. The headline change: the galaxy visualiser surfaces are descoped

**What.** The five `/api/galaxy/*` routes (`view`, `system/{id}`,
`system/name/{name}`, `search`, `stats`) and `cmd/galaxy-exporter` are no
longer ported to PostgreSQL. They are **retired**: routes and handlers
deleted (MR3), exporter command deleted (MR5). Everything else in the plan —
the powerplay modal factions/stations port, the survey route port, fallback
and wiring removal, diagnostics replacement, MCP verification, and artifact
cleanup — proceeds, amended per section 3 below.

**Why.** During the review we established the actual consumer graph:

- `/api/galaxy/view` has **no caller anywhere** — the frontend galaxy map
  loads bulk data from the static binary artifact (`useBinaryLoader.js`),
  which superseded the viewport API;
- `stats`, `search`, and `system/*` are called only by the galaxy visualiser
  page, and `App.jsx` routes `/galaxy` and `/galaxy/*` to `ComingSoon` — the
  page is unreachable by users;
- the exporter is dormant and undeployed (its Ansible role is an empty
  skeleton).

**Owner decision (David, 2026-07-25):** the five routes have no external
consumers. Recorded here and in the plan (Amendment record and 4.2) as
established fact by decision authority; later reviewers do not reopen it.

Meanwhile, porting those surfaces contributed **every production-scale
blocker** in the review:

- B1: the viewport's exact `count(*)` at maximum bounds extrapolates to
  80–120 s at ~160M rows — over the 15-second reader statement timeout, not
  just the 2-second budget;
- B2: the exact full-galaxy stats aggregates are marginal-to-over the
  15-second per-statement limit on cold cache, with no sanctioned fallback;
- B3/B4: a faithful exporter port would stream a 126M-row universe into a
  ~2.5 GB artifact the browser loader cannot hold, through a cursor design
  that dies at the statement timeout unless DECLARE/FETCH batching is pinned;
- B7: the pinned search `LIKE` pattern introduced wildcard injection and
  could defeat its own index.

Retiring instead of porting removes all of these while still achieving the
plan's actual goal: `internal/httpapi/galaxy.go` and `cmd/galaxy-exporter`
are the two largest importers of `internal/memgraph`, and deleting them is
the shortest path to a Memgraph-free build. Nothing user-facing changes —
these were dark surfaces.

**What we did not throw away.** The visualiser relaunch work is recorded in
the plan's new **section 8 (Deferred: Galaxy Visualiser Relaunch Register)**:
export membership predicate, DECLARE/FETCH pipeline redesign, stats snapshot
mechanism, search escaping, the full system-detail field map (including the
`BodyInfo.ID64` synthesis formula, hotspot display-name reverse map, and
station-stub/depot row rules), and the viewport-API port-or-drop decision.
Un-ComingSoon-ing `/galaxy` requires that plan; this one no longer blocks on
it.

## 2. Disposition of review findings

### Withdrawn by the descope (no longer applicable)

B1 (viewport count), B2 (stats refresh), B3 (exporter row universe),
B4 (DECLARE/FETCH), B7 (search LIKE injection — as a blocker in this plan),
H5 (golden-test gzip determinism, RSS projection), M7 (worst-case viewport
EXPLAIN gates), M8 (Caddy `precompressed`, cross-file atomicity, spool
placement). All are captured in section 8 for the visualiser plan. One
carry-forward: the same unescaped LIKE pattern exists in
`internal/galaxystore/search.go:50` and is live via MCP search today — per
the reviewer's condition it is **fixed in MR1 of this plan** (escaping plus
focused tests), not merely ticketed.

### Accepted and folded into the amended plan

| Finding | Amendment |
|---|---|
| B5 — blanket "`[]` never `null`" rule was itself a wire change | MR-D2 rewritten: empty-collection encoding pinned per surface from MR0 fixtures; zero-`time.Time` emission rule added |
| B6 (modal subset) — depot rows and `station_stub` population | 4.1 pins `kind = 'station'`, stub UNION (dedup by name, full row wins), carrier-filter no-op expectation; MR0 fixtures must include a stub system and a depot system |
| B8 — fixture provenance undefined; survey `last_update` remap broke MR4 parity | MR0 defines shape fixtures (legacy, local Memgraph stack) vs value fixtures (seeded relational DB, independently-known values); 4.5 declares the `last_update` remap a deviation and MR4 parity is defined against the relational expression |
| H1 — Kaine system prompt still teaches Cypher (live, wrong today) | Added to 2.1 inventory and MR6 deliverables |
| H2 — MR6 gate could not see its own targets | MR6 gate is now repo-wide with the MR9 removal list as the only exclusions, plus the edin-frontend launcher named explicitly |
| H3 — case-insensitive `galaxystore` resolver reuse trap | 4.1 forbids reusing `GetSystemFullByName`; exact-match resolver required; wrong-case fixtures in MR0/MR1/MR2 gates |
| H4 — MR9 had no gate (untested production build) | MR9 gate added: full build/test, final repo-wide grep, dark deploy of the MR9 binary with MR8 checks 1/2/5/8 re-run before public cutover |
| H6 — MR7 harness reached 7 of 19 tools; DSNs unnamed | MR7 names all three DSNs (galaxy_reader, EDIN app DB, raw history) and requires one invocation per individual current-galaxy tool |
| H7 — W5.6 evidence note still defers routes to W8 | Scheduled as an MR0 cross-document correction (with the delivery-plan W8/8.1 wording) |
| M1 — eddn-listener env-gated Memgraph write path | Named in new section 2.2a as an accepted residual with delivery-plan follow-up; Goal 5 scoped accordingly |
| M2 — "lock-step" allowlist guard doesn't exist; MR6/MR9 double-ownership | 4.6 restates the invariant (container values ⊆ sidecar allowlist), requires a mechanical test, assigns the sidecar edit to MR6 once; MR9 verifies only |
| M3 — bot/API skew degrades the whole diagnose report | MR8 pins control-api + bot to the same deploy step with the degradation window documented |
| M4 — MR-D3 claimed the history connection is read-only (it is `eddn_admin`) | MR-D3 reworded to "separate"; reader-role move tracked separately; MR8 evidence must not claim read-only |
| M5 (modal subset) — zero-timestamp emission | Pinned in MR-D2 and 4.1; visualiser-only field pins moved to section 8 |
| M6 — survey pins (dedupe by name, null-distance sorts first, DB-error→400, `mining_maps_used` discrepancy) | All pinned in 4.5 and the MR4 gate; `cube_enlarge` LATERAL construct pinned in 4.5 step 4 |
| M9 — "production-shaped database" undefined; deviations undeclared | MR-D6 defines it (new host `eddn_raw`, read-only as `galaxy_reader`, permitted from MR1); ordering/404 deviations enumerated in MR-D2 |
| M10 — MR9 rows drifted (empty exporter role, missing Make targets, unnamed testutil/`docker/memgraph`) | MR9 deliverables re-enumerated against the verified tree, including `internal/testutil/memgraph*` (which gates Neo4j driver removal) and the firewall allow/deny distinction |
| Low findings (default-privilege sweep, per-statement timeout clarification, dev-mode probe behaviour) | MR8 check 3 privilege sweep; MR-D5 timeout note; 4.6 dev-mode note |

### Rejected

None. Every review finding was either accepted into the amendment or
descoped with the surface it applied to.

## 3. Net effect on the waterfall

- MR0: fixture scope shrinks to modal + survey; provenance rules added;
  cross-document corrections added.
- MR1: `galaxystore` additions shrink to factions, stations (stub/depot
  rules), survey candidates, diagnostic probe.
- MR2, MR4, MR6, MR7, MR8: same shape, amended gates as above.
- MR3, MR5: ports become deletions.
- MR9: gains a real gate.
- The plan sheds its two hardest engineering problems (production-scale
  spatial counting; a multi-gigabyte export pipeline) and becomes a
  contract-faithful port of two live modal actions and one internal route,
  plus a clean demolition.

## 4. Reviewer conditions on the amended draft (2026-07-25) — both accepted

The reviewer approved the descope in principle subject to two amendments;
both are applied:

1. **Build-tagged Memgraph integration tests.**
   `internal/httpapi/kaine_integration_test.go` (tag `integration`) and
   `kaine_search_integration_test.go` (tag `integration_search`) escape the
   ordinary `go test ./...` gates and reference the `Server.memgraph` field.
   Disposition is pinned in **MR6** (not MR9 — MR6 deletes the field they
   reference, so they must be handled in that commit): the `integration`
   file is deleted (it already fails to compile against the current
   Anthropic SDK and `memgraph.Client`, and targets the frozen old server);
   the `integration_search` file is rewritten against seeded relational
   PostgreSQL without `testutil.StartTestMemgraph`. Tagged compile/test
   gates are added to MR6 and repeated in the MR9 gate.
2. **Live wildcard-search defect.** The unescaped LIKE pattern in
   `internal/galaxystore/search.go:50` is reachable through MCP search today,
   so it is fixed in **MR1** with focused escaping tests (`%`, `_`, `\`
   inputs match literally) rather than deferred to the visualiser register.
   Section 8 item 4 now records the fix's new home.

Final clarifications (2026-07-25), both applied:

- the rewritten `integration_search` test pins `GALAXY_TEST_DSN` (the MR7
  variable) and fails — never skips — when it is unset;
- the escape character is explicit in the SQL (`… ESCAPE '\'`), and the MR1
  gate requires checked-in `EXPLAIN` evidence that an escaped leading-`%`
  input still drives `idx_catalog_name_prefix`.

## 5. Status

**APPROVED FOR LOCAL EXECUTION by David on 2026-07-25.** Execute MR0-MR7 and
stop before MR8. No deployment or production mutation is authorised by this
approval. The review's MR0-gating findings are all resolved in the amendment
or removed with their surface.
