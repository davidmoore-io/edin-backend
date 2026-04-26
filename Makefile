.PHONY: build test lint build-api build-exporter
.PHONY: build-edin-bot build-docker-inspect-sidecar
.PHONY: test-edin-bot test-edin-bot-integration test-edin-bot-all test-edin-bot-cover lint-edin-bot
.PHONY: quick-dev dev-setup dev-keys dev-redis dev-ngrok dev-run dev-stop

# =============================================================================
# BUILD
# =============================================================================

build:
	go build ./...

build-api:
	CGO_ENABLED=0 go build -o bin/control-api ./cmd/control-api

build-exporter:
	CGO_ENABLED=0 go build -o bin/galaxy-exporter ./cmd/galaxy-exporter

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

test:
	go test ./...

lint:
	golangci-lint run ./...

# =============================================================================
# LOCAL DEV
# =============================================================================
# Primary command: make quick-dev
#   - Generates JWT signing keys (if missing)
#   - Starts Redis container (if not running)
#   - Starts ngrok tunnel (if not running)
#   - Builds and runs the backend with .env.local
#
# First run: copy .env.dev → .env.local and fill in secrets (Frontier creds etc.)
# Stop everything: make dev-stop

NGROK_DOMAIN := edin-dev.crossmoore.io.ngrok.app
ENV_FILE     := .env.local

quick-dev: dev-setup build-api dev-run

dev-setup: dev-env dev-keys dev-redis dev-ngrok

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
	@echo ""
	@echo "  Backend: http://localhost:8080"
	@echo "  ngrok:   https://$(NGROK_DOMAIN)"
	@echo ""
	@set -a && . ./$(ENV_FILE) && set +a && exec ./bin/control-api

dev-stop:
	@docker stop edin-dev-redis 2>/dev/null && docker rm edin-dev-redis 2>/dev/null || true
	@podman stop edin-dev-ngrok 2>/dev/null && podman rm edin-dev-ngrok 2>/dev/null || true
	@pkill -f "bin/control-api" 2>/dev/null || true
	@echo "Dev services stopped"
