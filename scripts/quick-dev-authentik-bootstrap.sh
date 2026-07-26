#!/usr/bin/env bash
set -euo pipefail

backend_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${backend_dir}/.dev-state"
secrets_file="${state_dir}/secrets.env"
base_url="${QUICK_DEV_AUTHENTIK_URL:-http://127.0.0.1:9000}"

if [[ ! -r "${secrets_file}" ]]; then
  echo "Missing ${secrets_file}; run scripts/quick-dev-secrets.sh first." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "${secrets_file}"
set +a

api() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local response_file status
  local args=(-sS -X "${method}" -H "Authorization: Bearer ${AUTHENTIK_BOOTSTRAP_TOKEN}")
  if [[ -n "${body}" ]]; then
    args+=(-H "Content-Type: application/json" --data "${body}")
  fi
  response_file="$(mktemp)"
  if ! status="$(curl "${args[@]}" -o "${response_file}" -w '%{http_code}' "${base_url}${path}")"; then
    rm -f "${response_file}"
    echo "Authentik API transport failed: ${method} ${path}" >&2
    return 1
  fi
  if [[ "${status}" != 2* ]]; then
    rm -f "${response_file}"
    echo "Authentik API ${status}: ${method} ${path}" >&2
    return 1
  fi
  cat "${response_file}"
  rm -f "${response_file}"
}

first_pk() {
  api GET "$1" | jq -er '.results[0].pk'
}

wait_for_api() {
  for _ in {1..120}; do
    if api GET "/api/v3/core/applications/?page_size=1" >/dev/null 2>&1 &&
      api GET "/api/v3/flows/instances/?slug=default-source-enrollment" |
        jq -e '.pagination.count > 0' >/dev/null 2>&1 &&
      api GET "/api/v3/stages/identification/" |
        jq -e '.pagination.count > 0' >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Local Authentik API and default blueprints did not become ready." >&2
  return 1
}

ensure_group() {
  local name="$1"
  local count
  count="$(api GET "/api/v3/core/groups/?name=${name}" | jq -r '.pagination.count')"
  if [[ "${count}" == "0" ]]; then
    api POST "/api/v3/core/groups/" "$(jq -nc --arg name "${name}" '{name:$name,is_superuser:false}')" >/dev/null
  fi
}

upload_brand_asset() {
  local path="$1"
  local name="$2"
  local status
  status="$(
    curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
      -H "Authorization: Bearer ${AUTHENTIK_BOOTSTRAP_TOKEN}" \
      --get --data-urlencode "name=${name}" --data-urlencode "usage=media" \
      "${base_url}/api/v3/admin/file/"
  )"
  if [[ "${status}" != 2* ]]; then
    echo "Authentik managed asset removal failed (${status}): ${name}" >&2
    return 1
  fi
  status="$(
    curl -sS -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer ${AUTHENTIK_BOOTSTRAP_TOKEN}" \
      -F "file=@${path}" \
      -F "name=${name}" \
      -F "usage=media" \
      "${base_url}/api/v3/admin/file/"
  )"
  if [[ "${status}" != 2* ]]; then
    echo "Authentik asset upload failed (${status}): ${name}" >&2
    return 1
  fi
}

wait_for_api

upload_brand_asset "${backend_dir}/assets/authentik/edin-logo.png" "edin-logo.png"
upload_brand_asset "${backend_dir}/assets/authentik/cosmos-field-nircam.jpg" "cosmos-field-nircam.jpg"
brand_uuid="$(
  api GET "/api/v3/core/brands/" |
    jq -er '.results[] | select(.default == true) | .brand_uuid'
)"
api PATCH "/api/v3/core/brands/${brand_uuid}/" \
  "$(jq -nc '{
    branding_title:"EDIN",
    branding_logo:"edin-logo.png",
    branding_favicon:"edin-logo.png",
    branding_default_flow_background:"cosmos-field-nircam.jpg",
    branding_custom_css:"",
    attributes:{settings:{theme:{base:"dark"}}}
  }')" >/dev/null

for flow_slug in \
  default-authentication-flow \
  default-source-authentication \
  default-source-enrollment \
  default-provider-authorization-explicit-consent \
  default-provider-authorization-implicit-consent \
  default-provider-invalidation-flow \
  default-invalidation-flow; do
  api PATCH "/api/v3/flows/instances/${flow_slug}/" \
    '{"background":"cosmos-field-nircam.jpg"}' >/dev/null
done

enrollment_flow="$(first_pk "/api/v3/flows/instances/?slug=default-source-enrollment")"
authentication_flow="$(first_pk "/api/v3/flows/instances/?slug=default-source-authentication")"
authorization_flow="$(first_pk "/api/v3/flows/instances/?slug=default-provider-authorization-implicit-consent")"
invalidation_flow="$(first_pk "/api/v3/flows/instances/?slug=default-provider-invalidation-flow")"
signing_key="$(first_pk "/api/v3/crypto/certificatekeypairs/?name=authentik%20Self-signed%20Certificate")"

discord_count="$(api GET "/api/v3/sources/oauth/?slug=discord" | jq -r '.pagination.count')"
discord_body="$(
  jq -nc \
    --arg auth "${authentication_flow}" \
    --arg enrollment "${enrollment_flow}" \
    --arg client_id "${AUTHENTIK_DISCORD_CLIENT_ID}" \
    --arg client_secret "${AUTHENTIK_DISCORD_CLIENT_SECRET}" \
    '{
      name:"Discord",
      slug:"discord",
      enabled:true,
      authentication_flow:$auth,
      enrollment_flow:$enrollment,
      policy_engine_mode:"any",
      user_matching_mode:"identifier",
      provider_type:"discord",
      consumer_key:$client_id,
      consumer_secret:$client_secret,
      additional_scopes:"guilds guilds.members.read"
    }'
)"
if [[ "${discord_count}" == "0" ]]; then
  api POST "/api/v3/sources/oauth/" "${discord_body}" >/dev/null
else
  api PATCH "/api/v3/sources/oauth/discord/" "${discord_body}" >/dev/null
fi
discord_pk="$(first_pk "/api/v3/sources/oauth/?slug=discord")"

identification_pk="$(first_pk "/api/v3/stages/identification/")"
api PATCH "/api/v3/stages/identification/${identification_pk}/" \
  "$(jq -nc --arg source "${discord_pk}" '{
    user_fields:[],
    sources:[$source],
    show_source_labels:true
  }')" >/dev/null

for group in \
  kaine-god kaine-approved kaine-directors kaine-lead-ops kaine-ops \
  kaine-pledge kaine-objectives-editor kaine-intel-viewer kaine-intel-full \
  kaine-mining-viewer kaine-mining-editor kaine-chat kaine-chat-debug \
  edin-copilot edin-copilot-trusted; do
  ensure_group "${group}"
done

groups_mapping_count="$(
  api GET "/api/v3/propertymappings/provider/scope/?scope_name=groups" |
    jq -r '.pagination.count'
)"
if [[ "${groups_mapping_count}" == "0" ]]; then
  groups_mapping="$(
    api POST "/api/v3/propertymappings/provider/scope/" \
      "$(jq -nc '{
        name:"Quick Dev Groups Scope",
        scope_name:"groups",
        description:"Local development grants full Kaine access to authenticated users",
        expression:"return [\"kaine-god\"] + [group.name for group in user.ak_groups.all()]"
      }')"
  )"
  groups_mapping_pk="$(jq -er '.pk' <<<"${groups_mapping}")"
else
  groups_mapping_pk="$(first_pk "/api/v3/propertymappings/provider/scope/?scope_name=groups")"
  api PATCH "/api/v3/propertymappings/provider/scope/${groups_mapping_pk}/" \
    "$(jq -nc '{
      expression:"return [\"kaine-god\"] + [group.name for group in user.ak_groups.all()]"
    }')" >/dev/null
fi

scope_mappings="$(
  api GET "/api/v3/propertymappings/provider/scope/?managed__startswith=goauthentik.io/providers/oauth2/scope-" |
    jq -c --arg groups "${groups_mapping_pk}" '[.results[].pk] + [$groups]'
)"

provider_count="$(api GET "/api/v3/providers/oauth2/?name=kaine-portal-provider" | jq -r '.pagination.count')"
provider_body="$(
  jq -nc \
    --arg authorization_flow "${authorization_flow}" \
    --arg invalidation_flow "${invalidation_flow}" \
    --arg signing_key "${signing_key}" \
    --argjson mappings "${scope_mappings}" \
    '{
      name:"kaine-portal-provider",
      authorization_flow:$authorization_flow,
      invalidation_flow:$invalidation_flow,
      client_type:"public",
      grant_types:["authorization_code","refresh_token"],
      client_id:"kaine-portal",
      redirect_uris:[
        {matching_mode:"strict",url:"http://127.0.0.1:3090/kaine/callback"},
        {matching_mode:"strict",url:"http://localhost:3090/kaine/callback"},
        {matching_mode:"strict",url:"http://localhost:5173/kaine/callback"}
      ],
      access_code_validity:"minutes=1",
      access_token_validity:"hours=1",
      refresh_token_validity:"days=30",
      include_claims_in_id_token:true,
      sub_mode:"hashed_user_id",
      issuer_mode:"per_provider",
      signing_key:$signing_key,
      property_mappings:$mappings
    }'
)"
if [[ "${provider_count}" == "0" ]]; then
  provider="$(api POST "/api/v3/providers/oauth2/" "${provider_body}")"
  provider_pk="$(jq -er '.pk' <<<"${provider}")"
else
  provider_pk="$(first_pk "/api/v3/providers/oauth2/?name=kaine-portal-provider")"
  api PATCH "/api/v3/providers/oauth2/${provider_pk}/" "${provider_body}" >/dev/null
fi

application_count="$(api GET "/api/v3/core/applications/?slug=kaine-portal" | jq -r '.pagination.count')"
application_body="$(
  jq -nc --arg provider "${provider_pk}" '{
    name:"Kaine Powerplay Portal (Quick Dev)",
    slug:"kaine-portal",
    provider:$provider,
    meta_launch_url:"http://127.0.0.1:3090/kaine/",
    meta_description:"Local EDIN development identity provider",
    policy_engine_mode:"any",
    open_in_new_tab:false
  }'
)"
if [[ "${application_count}" == "0" ]]; then
  api POST "/api/v3/core/applications/" "${application_body}" >/dev/null
else
  api PATCH "/api/v3/core/applications/kaine-portal/" "${application_body}" >/dev/null
fi

echo "Local Authentik configured at ${base_url}"
