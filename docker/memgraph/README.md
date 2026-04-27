# Local Memgraph

Configuration for the developer-machine Memgraph used during the manual end-to-end loop.

## Why this exists

Tests don't use this directory — Go integration tests spin ephemeral containers
via `internal/testutil/memgraph.go`. This compose stack is only for the
human-in-the-loop step: running the full backend + frontend against a populated
local graph and clicking around in a browser.

## Source of truth

The production Memgraph schema (indexes, constraints, seed Power nodes) lives in:

```
../edin-data/ansible/roles/databases/templates/memgraph-init.cypher.j2
```

`render-init.sh` reads that template, strips the optional auth block (local dev
runs unauthenticated), and writes `init.cypher` here. The output is gitignored
so nobody edits the rendered copy by hand.

If the parity matters to you (it should), `render-init.sh` runs as part of
`make memgraph-local`, so every local boot uses the freshest production schema.

## Usage

```
make memgraph-local         # render init + bring container up + wait healthy
make memgraph-local-down    # stop and remove the container (preserves volume)
make memgraph-local-seed    # load the dev-subset of production data (see edin-data/scripts/dev/)
```

The data volume `edin-dev-memgraph-data` persists between `up`/`down` cycles.
To wipe it: `docker volume rm edin-dev-memgraph-data` after `make memgraph-local-down`.

## Connecting

```
mgconsole --host 127.0.0.1 --port 7688
```

Or from the Go backend, set `MEMGRAPH_HOST=127.0.0.1 MEMGRAPH_PORT=7688`
(no auth needed locally).

The host port is **7688** (not the Memgraph default 7687) to avoid colliding
with other local Memgraph instances some developers already run on this box.
Inside the container Memgraph still listens on 7687.

## Pinning

The compose file pins `memgraph/memgraph:3.8.1` because that is what production
ran at the time of writing. If you discover production has moved, update the
pin and re-test — Tantivy tokenizer behaviour can shift between minor versions.
