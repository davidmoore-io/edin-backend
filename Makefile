.PHONY: build test test-integration lint build-api
.PHONY: build-edin-bot build-docker-inspect-sidecar
.PHONY: test-edin-bot test-edin-bot-integration test-edin-bot-all test-edin-bot-cover lint-edin-bot
.PHONY: quick-dev dev-setup dev-env dev-secrets dev-keys dev-data dev-commander-db \
	dev-authentik dev-redis dev-ngrok dev-run dev-status dev-stop

# =============================================================================
# BUILD
# =============================================================================

build:
	go build ./...

build-api:
	CGO_ENABLED=1 go build -o bin/control-api ./cmd/control-api

# ---- edin-bot targets ----
build-edin-bot:
	CGO_ENABLED=0 go build -o bin/edin-bot ./cmd/edin-bot

build-docker-inspect-sidecar:
	CGO_ENABLED=0 go build -o bin/docker-inspect-sidecar ./cmd/docker-inspect-sidecar

# Unit tests (fast; no integration tag, no DB required).
test-edin-bot:
	go test ./internal/edinbot/... ./cmd/edin-bot/... ./cmd/docker-inspect-sidecar/...

# Integration tests (testcontainers-backed; gated by build tag).
test-edin-bot-integration:
	go test -tags integration ./internal/edinbot/...

# Both layers — used by every implementation plan task gate.
test-edin-bot-all: test-edin-bot test-edin-bot-integration

test-edin-bot-cover:
	go test -cover -coverprofile=coverage.out ./internal/edinbot/...

lint-edin-bot:
	golangci-lint run ./internal/edinbot/... ./cmd/edin-bot/... ./cmd/docker-inspect-sidecar/...

# E2E test against REAL Discord. Requires EDIN_E2E=1, EDIN_BOT_TOKEN,
# EDIN_E2E_TEST_CHANNEL_ID set in the environment.
test-edin-bot-e2e:
	EDIN_E2E=1 go test -tags e2e ./internal/edinbot/ -run TestE2E -v

test:
	go test ./...

# Integration tests across the whole backend. Relational search tests require
# GALAXY_TEST_DSN and deliberately fail when it is absent.
test-integration:
	go test -tags 'integration integration_search' ./...

lint:
	golangci-lint run ./...

# =============================================================================
# LOCAL DEV
# =============================================================================
# Primary command: make quick-dev
#   - Starts both local PostgreSQL databases using their protected existing volumes
#   - Starts Redis and a namespaced local Authentik stack
#   - Bootstraps Discord login and the local Kaine OAuth application
#   - Loads the Anthropic/identity secrets from Ansible vault without printing them
#   - Starts ngrok for the EDIN Client Frontier callback
#   - Builds and runs control-api, EDDN listener, relational writer, and frontend
#
# First run: copy .env.dev → .env.local and fill in secrets (Frontier creds etc.)
# Stop everything: make dev-stop

NGROK_DOMAIN := edin-dev.crossmoore.io.ngrok.app
ENV_FILE     := .env.local

quick-dev: dev-setup build-api dev-run

dev-setup: dev-env dev-secrets dev-keys dev-data dev-commander-db dev-redis dev-authentik dev-ngrok

dev-env:
	@if [ ! -f $(ENV_FILE) ]; then \
		cp .env.dev $(ENV_FILE); \
		echo ""; \
		echo "══════════════════════════════════════════════════════════════"; \
		echo "  Created $(ENV_FILE) from template."; \
		echo "  Fill in FRONTIER_CLIENT_ID and FRONTIER_CLIENT_SECRET,"; \
		echo "  then re-run: make quick-dev"; \
		echo "══════════════════════════════════════════════════════════════"; \
		echo ""; \
		exit 1; \
	fi

dev-secrets:
	@./scripts/quick-dev-secrets.sh >/dev/null
	@echo "  ✓ Vaulted quick-dev secrets loaded"

dev-keys:
	@mkdir -p dev-keys
	@if [ ! -f dev-keys/commander-private.pem ]; then \
		openssl genrsa -out dev-keys/commander-private.pem 2048 2>/dev/null; \
		openssl rsa -in dev-keys/commander-private.pem -pubout \
			-out dev-keys/commander-public.pem 2>/dev/null; \
		echo "  ✓ Generated dev JWT signing keys"; \
	else \
		echo "  ✓ Dev keys exist"; \
	fi

dev-data:
	@$(MAKE) -C ../edin-data local-db-up >/dev/null
	@echo "  ✓ Local EDIN and EDDN databases running"

dev-commander-db:
	@./scripts/quick-dev-commander-db.sh >/dev/null
	@echo "  ✓ Local commander database roles ready"

dev-redis:
	@if docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^edin-dev-redis$$'; then \
		echo "  ✓ Redis running"; \
	elif docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q '^edin-dev-redis$$'; then \
		docker start edin-dev-redis >/dev/null; \
		echo "  ✓ Redis restarted"; \
	else \
		docker run -d --name edin-dev-redis -p 6379:6379 redis:7-alpine >/dev/null 2>&1; \
		echo "  ✓ Redis started (localhost:6379)"; \
	fi

dev-authentik:
	@docker compose -f docker-compose.quick-dev.yml \
		--env-file .dev-state/secrets.env up -d
	@./scripts/quick-dev-authentik-bootstrap.sh
	@echo "  ✓ Local Authentik running and configured"

dev-ngrok:
	@if podman ps --format '{{.Names}}' 2>/dev/null | grep -q '^edin-dev-ngrok$$'; then \
		echo "  ✓ ngrok running (https://$(NGROK_DOMAIN))"; \
	else \
		podman rm -f edin-dev-ngrok 2>/dev/null || true; \
		podman run -d --name edin-dev-ngrok \
			--network host \
			-v $(HOME)/.config/ngrok/ngrok.yml:/etc/ngrok.yml:ro,z \
			docker.io/ngrok/ngrok:latest \
			http 8080 --url=$(NGROK_DOMAIN) --config=/etc/ngrok.yml >/dev/null 2>&1; \
		sleep 3; \
		if podman ps --format '{{.Names}}' 2>/dev/null | grep -q '^edin-dev-ngrok$$'; then \
			echo "  ✓ ngrok started (https://$(NGROK_DOMAIN))"; \
		else \
			echo "  ✗ ngrok failed to start"; \
			echo "    Check: podman logs edin-dev-ngrok"; \
		fi; \
	fi

dev-run:
	@./scripts/quick-dev-run.sh

dev-status:
	@echo "Quick-dev containers:"
	@docker ps --format '{{.Names}}\t{{.Status}}\t{{.Ports}}' | \
		grep -E '^(edin-dev-|edin-timescaledb|eddn-timescaledb|eddn-listener|galaxy-writer-local)' || true
	@echo ""
	@for url in \
		http://127.0.0.1:9000/-/health/ready/ \
		http://127.0.0.1:8080/health \
		http://127.0.0.1:9101/health \
		http://127.0.0.1:3090/; do \
		code=$$(curl -sS -o /dev/null -w '%{http_code}' "$$url" 2>/dev/null || true); \
		printf '%-55s %s\n' "$$url" "$${code:-down}"; \
	done

dev-stop:
	@if [ -f .dev-state/frontend.pid ]; then \
		kill "$$(cat .dev-state/frontend.pid)" 2>/dev/null || true; \
		rm -f .dev-state/frontend.pid; \
	fi
	@if [ -f .dev-state/backend.pid ]; then \
		kill "$$(cat .dev-state/backend.pid)" 2>/dev/null || true; \
		rm -f .dev-state/backend.pid; \
	fi
	@pkill -f '^.*/bin/control-api$$' 2>/dev/null || true
	@pkill -f '^.*node.*/vite.*--host 127\.0\.0\.1$$' 2>/dev/null || true
	@$(MAKE) -C ../edin-data local-live-stop >/dev/null 2>&1 || true
	@docker compose -f docker-compose.quick-dev.yml \
		--env-file .dev-state/secrets.env stop 2>/dev/null || true
	@docker stop edin-dev-redis 2>/dev/null || true
	@podman stop edin-dev-ngrok 2>/dev/null && podman rm edin-dev-ngrok 2>/dev/null || true
	@echo "Quick-dev services stopped; database and Authentik volumes were preserved"
