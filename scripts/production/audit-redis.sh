#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || { echo "Usage: $0 HOST" >&2; exit 2; }
host=$1

ssh -p "${SSH_PORT:-22}" -o BatchMode=yes -o ConnectTimeout=10 "$host" 'bash -s' <<'REMOTE'
set -euo pipefail
container=edin-redis
docker inspect "$container" >/dev/null
redis() { docker exec "$container" redis-cli --raw "$@"; }

echo '=== persistence configuration ==='
redis CONFIG GET appendonly
redis CONFIG GET save
redis INFO persistence | grep -E '^(loading|aof_enabled|aof_rewrite_in_progress|rdb_last_bgsave_status|aof_last_bgrewrite_status):'
echo '=== key counts ==='
redis INFO keyspace | grep '^db' || true
printf 'total_keys='; redis DBSIZE
printf 'persistent_keys='
redis --scan | while IFS= read -r key; do
  [[ $(redis PTTL "$key") -eq -1 ]] && printf '%s\n' "$key"
done | tee /tmp/edin-redis-persistent-keys.txt | wc -l
echo '=== persistent key names (review; presence means STOP and classify) ==='
cat /tmp/edin-redis-persistent-keys.txt
rm -f /tmp/edin-redis-persistent-keys.txt
REMOTE
