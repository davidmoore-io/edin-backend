# EDIN Backend

Go application services for the EDIN platform. Provides the REST/MCP API, Discord bot integration, and galaxy map data export.

## Services

| Service | Purpose | Port |
|---------|---------|------|
| Control API | REST API + MCP server for AI tools | 8080 |
| Discord Bot | Slash commands for powerplay, system lookups | — |
| Galaxy Exporter | Binary map data for the web galaxy viewer | systemd timer |
| Redis | Session store for Kaine portal auth | 6379 |

## Go Module

`github.com/edin-space/edin-backend`

Key packages:
- `internal/assistant` — AI conversation runner with Claude (compaction, tool orchestration)
- `internal/httpapi` — HTTP API endpoints + Kaine portal auth
- `internal/tools` — MCP tool implementations (galaxy queries, market data, expansion analysis)
- `internal/memgraph` — Graph database read client
- `internal/discord` — Discord bot command handlers

## Usage

```bash
# Build all binaries
make build

# Build individual services
make build-api
make build-bot
make build-exporter

# Run tests
make test

# Deploy
cd ansible
ansible-playbook -i inventories/prod/hosts.ini site.yml
ansible-playbook -i inventories/prod/hosts.ini site.yml --tags control_api
ansible-playbook -i inventories/prod/hosts.ini site.yml --tags discord_bot
```

## Docker Network

Joins both `edin-app-net` (own services) and `edin-data-net` (database access). Connects to Memgraph and TimescaleDB by container name, not VPN.

## Prerequisites

Requires [atlas](../atlas) (Docker, firewall, VPN) and [edin-data](../edin-data) (databases) to be deployed first.
