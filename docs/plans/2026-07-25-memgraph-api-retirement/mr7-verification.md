# MR7 Local Verification

Date: 2026-07-25

Status: **PASS - MR8 NOT STARTED**

## Regression

- `GOWORK=off go test -count=1 ./internal/galaxystore ./internal/tools ./internal/httpapi ./cmd/control-api`: pass.
- `GOWORK=off go test -count=1 ./...`: pass.
- `integration` tagged compile gate: pass.
- `integration_search` relational HTTP tests: pass.
- active-runtime Memgraph/Cypher source sweep: empty.
- `internal/tools` Memgraph import/client sweep: empty.

## Database Smoke

The checked-in `TestGalaxyRelationalToolsSmoke` ran against three local
connections:

- `galaxy_reader` session over the local production-shaped `galaxy.*` corpus;
- local EDIN application database with 153 mining maps;
- separate local raw EDDN history connection.

All current-galaxy tools, all three dual-source mining tools, both history
tools, and `describe_tool` passed. The same smoke proved:

- `galaxy_query SELECT current_user` returns `galaxy_reader`;
- `galaxy.system_catalog` is readable;
- `feed.messages` is denied;
- history tools fail without the separate history client.

The authoritative source classification and per-tool result are recorded in
`mr7-tool-source-manifest.md`.

## Survey Gate

The exact runtime candidate SQL was measured with all 135 local mining-map
anchors against 1,319,445 coordinate-bearing systems:

- 20-run p95: 236.43 ms;
- result rows: 2,245;
- `idx_catalog_loc` drove each qualifying lateral spatial probe;
- full output: `explain/mr4-survey-performance.txt`.

No production host, remote service, deployment, or database mutation was used.
