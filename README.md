# EDIN Backend

Go application services for the EDIN platform. Provides the REST/MCP API and
the source for the Discord bot deployed by Atlas.

Migration state (2026-07-23): the new host remains in public maintenance mode.
Backend deployment is mechanically disabled until the activation gate supplies
`backend_deployment_enabled=true`. New galaxy reads use PostgreSQL
`galaxy.*`; retained Memgraph code is test/compatibility-only and is not
deployed.

## Services

| Service | Purpose | Port |
|---------|---------|------|
| Control API | REST API + MCP server for AI tools | 8080 |
| Discord Bot source | Slash commands for powerplay and system lookups; deployed by Atlas | — |
| EDIN application Redis | Session store for Kaine portal auth | 6379 |

## Desktop And Identity Contracts

The Flutter desktop client authenticates through the control API's
backend-mediated Frontier PKCE flow. The control API issues the commander JWT
used by `/api/v1/ingest/events`, `/api/v1/commander/heartbeat`, commander chat
session routes, and `/api/copilot/chat/ws`. The client does not authenticate
through Authentik and does not connect directly to either database.

Authentik remains authoritative for Kaine browser OAuth, MCP OAuth, Copilot
group membership, and the bot's client-credentials identity. The backend
accepts the separate Kaine and bot issuer/audience contracts rendered by
Ansible and uses the Authentik admin API for commander group management.

## Go Module

`github.com/edin-space/edin-backend`

Key packages:
- `internal/assistant` — AI conversation runner with Claude (compaction, tool orchestration)
- `internal/httpapi` — HTTP API endpoints + Kaine portal auth
- `internal/tools` — MCP tool implementations (galaxy queries, market data, expansion analysis)
- `internal/memgraph` — legacy test/compatibility package; not deployed
- `internal/discord` — Discord bot command handlers

## Usage

```bash
# Start the complete local development stack
make quick-dev

# Inspect or stop it
make dev-status
make dev-stop

# Build all binaries
make build

# Build individual services
make build-api
make build-bot

# Run tests
make test

# Deploy after the backend activation gate only
cd ansible
ansible-playbook -i inventories/prod/hosts.ini site.yml \
  --limit new-edin-space \
  -e backend_deployment_enabled=true
ansible-playbook -i inventories/prod/hosts.ini site.yml \
  --limit new-edin-space \
  -e backend_deployment_enabled=true \
  --tags control_api

# Reconcile commander database roles as a separate, explicit operation.
BACKEND_DB_ROLES_CONFIRMED=1 make -C .. deploy-backend-db-roles
```

### Full Local Stack

`make quick-dev` starts the local EDIN and EDDN PostgreSQL databases, the EDDN
listener and relational galaxy writer, the EDIN application Redis, Authentik,
control API, MCP endpoint,
frontend, and the ngrok tunnel used by the EDIN Client Frontier callback.
Authentik 2026 is PostgreSQL-only and does not use this Redis instance. The
launcher loads development secrets from the existing Ansible vaults into the
ignored, mode-`0600`
`.dev-state/secrets.env`; secret values are not copied into tracked env files.
It also provisions non-superuser local commander reader/writer roles and runs
the embedded commander migrations, so journal ingest and commander tools use
the same row-level-security shape as production.

The frontend is available at `http://127.0.0.1:3090`, with browser OAuth served
entirely by the local Authentik instance at `http://127.0.0.1:9000`.
Authenticated local users receive the `kaine-god` development claim. The
Discord bot is deliberately excluded because it is a singleton and running a
local copy would duplicate production responses.

Kaine chat tools compose from Authentik groups. `kaine-approved` supplies the
galaxy/mining surface, `kaine-god` additionally supplies server operations,
and `edin-copilot` supplies the authenticated commander's journal/location
tools. A user in both Kaine and Copilot groups receives both surfaces;
`kaine-god` alone never grants private commander data.

`make dev-stop` stops processes and containers only. It never runs
`docker compose down`, `down -v`, or removes a volume. Existing EDIN/EDDN
database volumes and the namespaced `edin-dev-authentik-*` volumes are
preserved.

## Docker Network

Joins `edin-app-net` for application services and `edin-data-net` for
same-host PostgreSQL access. No production Memgraph or graph exporter is
deployed.

## Prerequisites

Requires [atlas](../atlas) (Docker, firewall, VPN) and [edin-data](../edin-data) (databases) to be deployed first.
