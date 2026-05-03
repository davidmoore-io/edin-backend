#!/usr/bin/env bash
# Render the production Memgraph init template into a local init.cypher.
#
# Why this script exists:
#   The production schema (indexes, constraints, seed Power nodes) lives in the
#   Ansible-rendered Jinja2 template in the edin-data repo. The only Jinja-sensitive
#   block is the optional auth setup. For local dev/test we want the same schema
#   without auth, so we strip the auth block and emit pure Cypher.
#
# This keeps a single source of truth (the j2 template); local dev cannot drift
# silently from production because this script reruns on every `make memgraph-local`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TEMPLATE="${REPO_ROOT}/../edin-data/ansible/roles/databases/templates/memgraph-init.cypher.j2"
OUTPUT="${REPO_ROOT}/docker/memgraph/init.cypher"

if [[ ! -f "$TEMPLATE" ]]; then
  echo "error: template not found at $TEMPLATE" >&2
  echo "       expected sibling edin-data repo at \$REPO_ROOT/../edin-data/" >&2
  exit 1
fi

# Strip the {% if memgraph_auth_enabled %} ... {% endif %} block.
# Local Memgraph runs without auth, so the entire conditional contents are dropped.
awk '
  /^{% if memgraph_auth_enabled/ { skip = 1; next }
  /^{% endif %}/                 { skip = 0; next }
  skip                            { next }
  { print }
' "$TEMPLATE" > "$OUTPUT"

echo "rendered: $OUTPUT"
