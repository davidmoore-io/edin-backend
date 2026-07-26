#!/usr/bin/env bash
set -euo pipefail

backend_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workspace_dir="$(cd "${backend_dir}/.." && pwd)"
vault_dir="${backend_dir}/ansible"
vault_file="${vault_dir}/group_vars/creds.yml"
atlas_vault_dir="${workspace_dir}/atlas/ansible"
atlas_vault_file="${atlas_vault_dir}/group_vars/creds.yml"
state_dir="${backend_dir}/.dev-state"
output="${state_dir}/secrets.env"

mkdir -p "${state_dir}" /tmp/edin-quick-dev-ansible
chmod 700 "${state_dir}"

backend_vault="$(
  ANSIBLE_LOCAL_TEMP=/tmp/edin-quick-dev-ansible \
    "${vault_dir}/.venv/bin/ansible-vault" view \
    --vault-password-file "${vault_dir}/.vault_pass.txt" \
    "${vault_file}"
)"

atlas_vault="$(
  ANSIBLE_LOCAL_TEMP=/tmp/edin-quick-dev-ansible \
    "${atlas_vault_dir}/.venv/bin/ansible-vault" view \
    --vault-password-file "${atlas_vault_dir}/.vault_pass.txt" \
    "${atlas_vault_file}"
)"

vault_value() {
  local source="$1"
  local key="$2"
  printf '%s\n' "${source}" |
    awk -F': ' -v key="${key}" '$1 == key {v=$2; gsub(/^"|"$/, "", v); print v}'
}

anthropic_key="$(vault_value "${backend_vault}" vault_anthropic_api_key)"
authentik_secret="$(vault_value "${atlas_vault}" vault_authentik_secret_key)"
authentik_postgres="$(vault_value "${atlas_vault}" vault_authentik_postgres_password)"
authentik_bootstrap_password="$(vault_value "${atlas_vault}" vault_authentik_bootstrap_password)"
authentik_bootstrap_token="$(vault_value "${atlas_vault}" vault_authentik_bootstrap_token)"
discord_secret="$(vault_value "${atlas_vault}" vault_discord_client_secret)"
unset backend_vault atlas_vault

for value_name in anthropic_key authentik_secret authentik_postgres \
  authentik_bootstrap_password authentik_bootstrap_token discord_secret; do
  if [[ -z "${!value_name}" ]]; then
    echo "Missing required quick-dev secret: ${value_name}" >&2
    exit 1
  fi
done

umask 077
{
  printf 'ANTHROPIC_API_KEY=%s\n' "${anthropic_key}"
  printf 'AUTHENTIK_SECRET_KEY=%s\n' "${authentik_secret}"
  printf 'AUTHENTIK_POSTGRES_PASSWORD=%s\n' "${authentik_postgres}"
  printf 'AUTHENTIK_BOOTSTRAP_PASSWORD=%s\n' "${authentik_bootstrap_password}"
  printf 'AUTHENTIK_BOOTSTRAP_TOKEN=%s\n' "${authentik_bootstrap_token}"
  printf 'AUTHENTIK_DISCORD_CLIENT_ID=%s\n' "1487253410658123808"
  printf 'AUTHENTIK_DISCORD_CLIENT_SECRET=%s\n' "${discord_secret}"
  printf 'QUICK_DEV_WORKSPACE=%s\n' "${workspace_dir}"
} >"${output}"

chmod 600 "${output}"
printf '%s\n' "${output}"
