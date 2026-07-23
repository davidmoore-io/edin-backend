# access_list role

access_list owns the on-host directories under `/var/log/edin/` where the
control-api container writes its denial-audit and admin-action logs.

## Adding a new commander

After plan Task 5, the flow is one click per commander:

1. **Have them log in once via copilot.** The Frontier OAuth callback
   auto-creates a shadow Authentik user under `users/edin-commanders/`,
   links it to their FID, and persists `approved=false`. They'll see a
   generic 403 on this first login — that's expected.
2. **Open the Kaine AdminPage → Commanders tab.** You'll find an
   "Awaiting approval" row for the new commander.
3. **Click "Grant edin-copilot"** (or "Grant trusted" if they need
   plasmium / LTD / schema mining tools). One click — this atomically
   sets `approved=true` AND adds them to the chosen Authentik group.
4. **Tell them to log out + log in.** Their next JWT carries the
   group-derived scopes; tool visibility on the copilot WebSocket
   reflects exactly what `edin-copilot` (or `edin-copilot-trusted`)
   grants.

Total operator effort: one click per commander after they log in once.
No env-var edits, no Ansible deploys, no manual Authentik-UI account
creation.

## Removing a commander

Click **"Revoke"** on their Commanders row.

The Revoke action atomically:
- flips `approved=false` in the DB
- removes them from `edin-copilot` AND `edin-copilot-trusted` in Authentik
- enumerates their per-FID JTI tracking set in Redis and adds every
  active jti to the revoked-jti set, so any live JWT they hold is
  rejected on its next request

Effective immediately. The commander's next copilot request returns 401;
their next login returns 403 `awaiting_approval`. No 24h JWT-expiry
window to wait through.

## Tailing the denial log

```bash
ssh -p 22 -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes debian@162.19.62.216 \
  'sudo tail -f /var/log/edin/login-attempts.log'
```

JSON lines. `reason` field discriminates:

- `not_on_allowlist` — unlinked + no env-var match (transitional; should
  not occur post-Task 5 since auto-link covers every callback)
- `awaiting_approval` — linked but `approved=false`
- `no_scopes_granted` — linked + approved but Authentik groups map to an
  empty scope set (group misconfiguration)
- `authentik_unreachable` — Authentik API call failed or timed out.
  Deny-closed.
- `authentik_user_missing` — linked but the referenced Authentik user
  has been deleted. Distinct from `_unreachable` so admins see the
  actionable state ("re-link this commander or unlink") vs the
  transient one.
- `link_persist_failed` — auto-link's Authentik shadow create succeeded
  but the DB-side link write failed. Self-healing: next callback's
  duplicate-username fallback recovers the same UUID and retries.

## Tailing the admin-actions audit

```bash
ssh -p 22 -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes debian@162.19.62.216 \
  'sudo tail -f /var/log/edin/admin-actions.log'
```

Every approve / deny / grant / revoke / link / unlink / revoke_sessions
mutation through the Kaine AdminPage Commanders tab writes a JSON line
here. This is the canonical "who changed what access" record — always
check this first when investigating a scope-related incident.

Shape per line:

```json
{"time":"2026-04-23T18:42:11Z","admin_sub":"auth0|...","admin_name":"david",
 "action":"commander.grant","subject_fid":"F2504",
 "details":{"group":"edin-copilot"},"ip":"..."}
```

Action values include `commander.grant`, `commander.revoke`,
`commander.link`, `commander.unlink`, `commander.approve`,
`commander.deny`, and `commander.grant.partial` (the latter fires
when AddUserToGroup succeeded but SetApproved failed — a partial
state where the user is in the group but not approved, so login is
still denied; admin should retry Grant).

Pipe to `jq` for analysis:

```bash
ssh -p 22 -i ~/.ssh/id_ed25519 -o IdentitiesOnly=yes debian@162.19.62.216 \
  'sudo cat /var/log/edin/admin-actions.log' \
  | jq 'select(.action == "commander.grant") | {fid: .subject_fid, group: .details.group}'
```

## Metrics of interest

Scrape Prometheus on the control-api at `:9090/metrics`. Relevant series:

- `edin_commander_access_decisions_total{reason="authentik_unreachable"}`
  — spike → Authentik incident; investigate the auth.edin.space upstream.
- `edin_commander_access_decisions_total{reason="awaiting_approval"}` —
  rate of pending-approval denials. A growing rate suggests new commanders
  are logging in but admins aren't keeping up with Granting them.
- `edin_commander_access_decisions_total{reason="authentik_user_missing"}`
  — points at broken link rows. Re-link or unlink the affected commanders
  via the Commanders tab.
- `edin_commander_access_resolution_latency_seconds{outcome="ok"}` p95 —
  SLO target < 250 ms. Anything above 1 s sustained means Authentik is
  slow even when reachable; the 2 s timeout will start firing as
  `authentik_unreachable` denials.

## Why a separate role and not group_vars?

Two reasons:

1. **Discoverability.** A new maintainer looking for "who can log in?"
   finds it via `roles/access_list/` rather than trawling a grab-bag
   `group_vars/all.yml`.
2. **Operational task surface.** The role owns the on-host log
   directory (`/var/log/edin/`), bind-mounted into the control-api
   container so denial + admin-action audits survive container rebuilds
   and are accessible to operators via plain `sudo tail` without
   `docker exec`.

## Why not in ansible-vault?

FIDs aren't secret. They appear in every commander's journal output, in any
EDIN JWT this backend issues, and in frontend presence responses. Encrypting
them would add friction without adding security.
