#!/usr/bin/env bash
set -euo pipefail

backend_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workspace_dir="$(cd "${backend_dir}/.." && pwd)"
frontend_dir="${workspace_dir}/edin-frontend"
data_dir="${workspace_dir}/edin-data"
state_dir="${backend_dir}/.dev-state"
secrets_file="${state_dir}/secrets.env"
backend_log="${state_dir}/backend.log"
frontend_log="${state_dir}/frontend.log"
backend_pid=""
frontend_pid=""

cleanup() {
  [[ -n "${frontend_pid}" ]] && kill "${frontend_pid}" 2>/dev/null || true
  [[ -n "${backend_pid}" ]] && kill "${backend_pid}" 2>/dev/null || true
  rm -f "${state_dir}/backend.pid" "${state_dir}/frontend.pid"
}
trap cleanup EXIT INT TERM

port_available() {
  local port="$1"
  ! timeout 1 bash -c "</dev/tcp/127.0.0.1/${port}" 2>/dev/null
}

for port in 8080 8081 3090; do
  if ! port_available "${port}"; then
    echo "Port ${port} is already in use. Run 'make dev-stop' before quick-dev." >&2
    exit 1
  fi
done

if [[ ! -r "${secrets_file}" ]]; then
  echo "Missing ${secrets_file}; run 'make dev-secrets'." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
# shellcheck disable=SC1090
source "${backend_dir}/.env.local"
# shellcheck disable=SC1090
source "${secrets_file}"
set +a

export SSG_HTTP_ADDR=127.0.0.1:8080
export SSG_MCP_ADDR=127.0.0.1:8081
export EDIN_DB_ENABLED=true
export EDIN_DB_HOST=127.0.0.1
export EDIN_DB_PORT=5432
export EDIN_DB_USER=edin_admin
export EDIN_DB_PASSWORD=eddn-local-dev
export EDIN_DB_NAME=edin
export EDIN_CMD_WRITER_DSN='postgres://edin_cmd_writer:edin-local-cmd-writer@127.0.0.1:5432/edin?sslmode=disable'
export EDIN_CMD_READER_DSN='postgres://edin_cmd_reader:edin-local-cmd-reader@127.0.0.1:5432/edin?sslmode=disable'
export EDIN_CMD_MIGRATOR_DSN='postgres://edin_admin:eddn-local-dev@127.0.0.1:5432/edin?sslmode=disable'
export EDDN_RAW_DB_ENABLED=true
export EDDN_RAW_DB_HOST=127.0.0.1
export EDDN_RAW_DB_PORT=5433
export EDDN_RAW_DB_USER=eddn_admin
export EDDN_RAW_DB_PASSWORD=eddn-local-dev
export EDDN_RAW_DB_NAME=eddn_raw
export GALAXY_READER_DSN='postgres://eddn_admin:eddn-local-dev@127.0.0.1:5433/eddn_raw?sslmode=disable&options=-c%20role%3Dgalaxy_reader'
export KAINE_AUTH_ENABLED=true
export KAINE_AUTH_CLIENT_ID=kaine-portal
export KAINE_AUTH_JWKS_URL=http://127.0.0.1:9000/application/o/kaine-portal/jwks/
export KAINE_AUTH_ISSUER=http://127.0.0.1:9000/application/o/kaine-portal/
export KAINE_AUTH_AUDIENCE=kaine-portal
export KAINE_AUTH_TOKEN_URL=http://127.0.0.1:9000/application/o/token/
export KAINE_AUTH_COOKIE_DOMAIN=
export KAINE_AUTH_COOKIE_SECURE=false
export AUTHENTIK_API_ENABLED=true
export AUTHENTIK_API_URL=http://127.0.0.1:9000
export AUTHENTIK_API_TOKEN="${AUTHENTIK_BOOTSTRAP_TOKEN}"

(
  cd "${backend_dir}"
  exec ./bin/control-api
) >"${backend_log}" 2>&1 &
backend_pid="$!"
printf '%s\n' "${backend_pid}" >"${state_dir}/backend.pid"

for _ in {1..60}; do
  if curl -fsS http://127.0.0.1:8080/health >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${backend_pid}" 2>/dev/null; then
    echo "control-api exited during startup:" >&2
    tail -n 80 "${backend_log}" >&2
    exit 1
  fi
  sleep 1
done
curl -fsS http://127.0.0.1:8080/health >/dev/null

make -C "${data_dir}" listener-local-start >/dev/null

(
  cd "${frontend_dir}"
  export VITE_API_PROXY_TARGET=http://127.0.0.1:8080
  export VITE_DEV_PORT=3090
  export VITE_AUTHENTIK_URL=http://127.0.0.1:9000
  export VITE_AUTHENTIK_CLIENT_ID=kaine-portal
  exec npm run dev -- --host 127.0.0.1
) >"${frontend_log}" 2>&1 &
frontend_pid="$!"
printf '%s\n' "${frontend_pid}" >"${state_dir}/frontend.pid"

for _ in {1..40}; do
  if curl -fsS http://127.0.0.1:3090/ >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${frontend_pid}" 2>/dev/null; then
    echo "Vite exited during startup:" >&2
    tail -n 80 "${frontend_log}" >&2
    exit 1
  fi
  sleep 1
done
curl -fsS http://127.0.0.1:3090/ >/dev/null

cat <<EOF

EDIN quick-dev is running:
  Frontend:  http://127.0.0.1:3090/
  Kaine:     http://127.0.0.1:3090/kaine/
  Authentik: http://127.0.0.1:9000/
  API:       http://127.0.0.1:8080/health
  MCP:       http://127.0.0.1:8081/mcp
  ngrok:     https://edin-dev.crossmoore.io.ngrok.app

Logs:
  ${backend_log}
  ${frontend_log}

Press Ctrl-C to stop the frontend and backend. Data, Authentik, Redis, and
listener containers remain stopped only by 'make dev-stop'.
EOF

wait "${backend_pid}" "${frontend_pid}"
