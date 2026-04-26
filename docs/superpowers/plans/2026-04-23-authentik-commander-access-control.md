# Commander Access Control via Authentik (Model B)

**Date:** 2026-04-23
**Status:** Proposed — reviewed (2 review passes, findings folded in)
**Approach:** Model B from the auth design review — Frontier PKCE keeps providing
"who are you"; Authentik provides "what can you do" via an optional commander ↔
Authentik-user link. No change to the commander-facing login UX.

**Review notes:** Multiple review passes against senior-dev / infosec
criteria plus a design update have folded the following in inline. Notable
ones, captured where relevant:

- **Auto-link on first Frontier login.** The callback auto-provisions a
  shadow Authentik user under `users/edin-commanders/` so admins never
  create accounts by hand. Commanders are always linked after Task 5;
  admins just Grant or Revoke (Task 8).
- **Grant / Revoke bundle approve + group in one click.** Admins don't
  bounce between the Commanders tab and the Users tab — Grant adds the
  chosen group AND sets approved=true; Revoke removes managed groups,
  flips approved=false, and calls revokeAllSessions. Narrow allow-list
  (`edin-copilot` / `edin-copilot-trusted` only) prevents a slip from
  handing out kaine-admin.
- Active sessions must be revocable on Deny / Unlink / group-removal /
  Revoke (not wait up to 24h for JWT expiry). Redis-backed per-FID `jti`
  set.
- `toolScopes` lookup is fail-closed — a missing tool entry is a hard
  error, not a silent "public tool."
- `commander.commanders` gets RLS now (not deferred), with a dedicated
  admin role that has `BYPASSRLS` only for the `commander` schema.
- Authentik is the trust anchor for scope assignment; documented
  explicitly.
- Admin mutations are audited to a dedicated file alongside login
  attempts.
- Several smaller hardening items (log-injection defense, duplicate-link
  409, authentik_user_missing reason, self-revocation confirmation, …)
  noted in tasks.

---

## Goal

Replace the env-var FID allowlist with an Authentik-group-backed access-control
model for Frontier-authenticated commanders. In the same stroke, decompose the
hardcoded `kaineAllowedTools` / `copilotAllowedTools` allow-maps into per-tool
scope requirements, so Kaine chat and the copilot converge on a single tool
surface gated by the caller's scope set.

## Non-goals

- **No** replacement of Frontier PKCE with Authentik-federated login. Commanders
  keep clicking "Login with Frontier" and never see an Authentik round-trip.
- **No** destructive removal of the access_list Ansible role. Its env-var
  allowlist stays as a live fallback until the final task retires it.
- **No** change to Kaine portal auth. Kaine users still log in via Authentik
  directly; their scope derivation gets tidied but not behaviourally altered.
- **No** change to the Flutter desktop-client poll-based PKCE flow, beyond the
  same scope-claim surface the web flow gets.

## Design

### Identity vs authorization

- **Identity** comes from Frontier PKCE. The EDIN JWT continues to carry `fid`,
  `name`, `jti`.
- **Authorization** comes from `commander.commanders.authentik_user_id`. If the
  column is set AND the row's `approved` flag is true, the callback queries
  Authentik for that user's group memberships, derives a scope set, and bakes
  that set into the EDIN JWT as a new `scopes` claim.
- **Fallback** — if the link is unset, the callback consults the env-var
  `COMMANDER_FID_ALLOWLIST`. Match → default commander scope set. Miss → 403.
  This path is removed in the final task.

### Scope model

Introduce three fine-grained scopes alongside the existing coarse ones:

| Scope | Guards |
|---|---|
| `galaxy_read` | All non-opinionated galaxy queries (systems, stations, bodies, signals, powerplay cycle, factions, markets, expansion queries, history, Spansh, route planning) |
| `kaine_mining` | Kaine-specific mining intelligence (plasmium_buyers, ltd_buyers, expansion_targets, galaxy_schema) |
| `commander_data` | Commander-scoped queries (commander_events, commander_location) |

The existing coarse scopes keep their role as endpoint gates:
- `kaine_chat` — allowed to open the Kaine WS
- `copilot_chat` — allowed to open the copilot WS
- `llm_operator`, `admin` — Discord / infrastructure tools

A commander's access to the copilot chat surface = `copilot_chat ∈ scopes`.
Their ability to invoke a given tool = `toolScopes[tool] ∈ scopes`.

### Group-to-scope mapping

| Authentik group | Granted scopes |
|---|---|
| `kaine-god` | admin, llm_operator, kaine_chat, galaxy_read, kaine_mining, commander_data |
| `kaine-approved` | kaine_chat, galaxy_read, kaine_mining |
| `kaine-chat` | kaine_chat, galaxy_read, kaine_mining |
| `kaine-chat-debug` | kaine_chat, galaxy_read, kaine_mining (+ debug tool-result visibility) |
| `edin-copilot` | copilot_chat, galaxy_read, commander_data |
| `edin-copilot-trusted` | copilot_chat, galaxy_read, commander_data, kaine_mining |

Unknown groups are ignored silently — future Authentik groups don't break the
system; they just grant nothing until the map is extended.

### Default commander scope set

When the env-var fallback grants access, the commander gets `{copilot_chat,
galaxy_read, commander_data}` — exactly today's effective permission set.

### Trust model

Authentik is the authoritative source of commander authorization. Compromise
of the Authentik instance equals compromise of the commander scope model: an
attacker with Authentik admin access could add any FID's linked user to
`edin-copilot-trusted` and escalate. Mitigations in force:

- Authentik listens only on the internal Docker network; external access is
  only via Caddy's `auth.edin.space` TLS vhost.
- Kaine admin access (which today also governs commander administration) is
  gated behind `kaine-admin` / `kaine-directors` groups set by a real human
  in the Authentik UI.
- The Kaine AdminPage's group-prefix guard (enforced at `kaine.go:1849/1882`)
  caps what an already-authenticated admin can do: they can only add or
  remove `kaine-*` / `edin-*` groups via the API, not arbitrary ones.

Any future move to expose Authentik over additional network paths, or to
federate trust with external OAuth providers, requires a fresh threat review.

### Session lifecycle and revocation

JWTs live 24h. A naive "flip `approved=false`" does not revoke an active
session — the commander keeps their existing JWT until it expires. For a
production access-control system that is not acceptable.

We track active JWTs per FID and revoke them explicitly on access changes:

- **On issue** (in `commander_auth.go` + `commander_client_auth.go`): after
  `commanderJWTIssuer.Issue` returns a new `jti`, `SADD commander:jtis:{fid}
  {jti}` with a TTL matching the JWT lifetime.
- **On logout** (existing `/api/commander/auth/logout`): adds `jti` to the
  revoked set AND removes it from the per-FID set.
- **On admin Deny / Unlink**: `SMEMBERS commander:jtis:{fid}` → add each to
  the revoked set with TTL = max remaining JWT lifetime → `DEL
  commander:jtis:{fid}`. Next request from any of those tokens hits the
  validator's revoked-jti check and returns 401.
- **On admin group-add for an already-logged-in commander**: no automatic
  revocation. Scope ADDITIONS are safe — the user just needs to re-login
  (or wait for JWT expiry) to pick them up. Admin UI surfaces this with a
  "refresh session" note.
- **On admin group-remove**: same revocation flow as Deny — the removed
  scope is privilege-reducing, so we force re-auth to make it effective
  immediately.

### Approval semantics

`approved=true` is only meaningful alongside a non-null `authentik_user_id`.
Setting `approved=true` on an unlinked commander is legal at the schema
level (the two columns are independent) but has no effect on auth: the
decision matrix falls through to the allowlist branch. The Admin UI
disables the "Approve" toggle on unlinked commanders and surfaces the
constraint with a tooltip. Documented here and in the handler GoDoc so
anyone reading the code isn't surprised.

### Auto-link on first Frontier login

The Frontier → Authentik link is established automatically on the
commander's first successful Frontier callback, not by manual admin
action. The callback handler ensures (fid, authentik_user_id) exists
before `resolveCommanderAccess` runs.

**Shadow user provisioning.** When the callback sees
`commander.commanders.authentik_user_id IS NULL`, it asks the Authentik
API to create a **shadow user** under the dedicated path
`users/edin-commanders/`:

- `username` = FID (e.g. `F2504`)
- `name` = commander name from Frontier (`sanitizeLogField`-clean)
- `email` = synthetic, non-deliverable: `{lowercased-fid}@edin.commanders.invalid`
  — the `.invalid` TLD is reserved by RFC 2606 and guaranteed not to
  resolve, so no one will ever mistakenly mail it.
- `path` = `users/edin-commanders`
- `is_active` = `true` (required for Authentik to consider group
  memberships, even for users who never log in)
- **No password, no linked OAuth source, no stage of any kind** — the
  shadow user has no credential that would let it sign into Authentik
  directly. It exists solely as a group-membership handle.
- Initial groups: **none**. The `edin-copilot` / `edin-copilot-trusted`
  group is added later by an admin's Grant action.

After the Authentik API returns the new user's UUID, the callback
persists `commander.commanders.authentik_user_id = <uuid>` via the
cmd_writer tx. The commander's own callback continues on: `approved`
defaults to `false`, so `resolveCommanderAccess` returns
`reason=awaiting_approval` on this first login, and the commander sees
the generic 403 page.

**Why shadow users, not real Authentik accounts.** The commander does
not need to sign into Authentik; Frontier is the identity source. A
shadow user gives us the one thing Authentik provides that matters here
— a stable handle for group membership — without forcing the commander
through a second OAuth round trip or requiring them to manage a second
set of credentials. The `users/edin-commanders/` path keeps them
visually and administratively separate from human Kaine admins, so a
future "prune unused shadow users" task can target them safely.

**Re-link semantics.** If an admin (e.g. David) wants to merge a
shadow into their existing real Authentik user, they can Unlink the
shadow (which triggers `revokeAllSessions`) and Link the FID to their
real user UUID via the admin endpoints. The shadow is left orphaned;
operator can delete it from the Authentik UI. This is an unusual
operation — the normal case is "auto-linked shadow stays linked
forever."

**Idempotency.** Every callback first reads the commander row; if a
link already exists, the Authentik create step is skipped. Concurrent
first-logins from the same FID are serialized by the `UPSERT … RETURNING`
on `commander.commanders` so only one callback path drives the
create-user call.

**Failure handling.** If the Authentik create-user call fails
(transient 5xx, timeout, network), the callback returns 500 to the
commander with a generic "try again" page — we do **not** let them in
without a link. The next retry either succeeds the create step or
retries forever until Authentik is healthy. No commander data is
written on a failed create: the FID stays unlinked, and the next
successful callback re-tries creation from scratch.

**Approval = Grant = approved + group in one click.** The admin
workflow against a linked-but-not-approved commander is a single
server action: **Grant**. The Grant endpoint sets `approved=true` AND
adds the chosen group (`edin-copilot` by default) to the shadow user
in Authentik. An equivalent **Revoke** endpoint reverses both steps
AND calls `revokeAllSessions`. The admin UI exposes these as single
buttons — the admin never needs to open the Users tab to edit group
membership for a shadow user by hand.

### Log-injection defense

Commander display names flow from CAPI into both server text logs and the
JSON audit file. `json.Marshal` in the audit path escapes newlines and
control characters. The text-log path (`logger.Warn(fmt.Sprintf(...))`)
does not — a malicious display name like `"Nice\n[FAKE] admin_approve
fid=F2504"` could otherwise forge log lines.

The denial / admin-action logging helpers sanitize free-text fields before
passing them to the text logger by replacing `\r\n` with `\\r\\n` via a
single `sanitizeLogField` helper. The structured JSON audit is the
authoritative forensic source; the text log is for operators tailing
`docker logs`.

### JWT shape

`CommanderClaims` grows one field:

```go
type CommanderClaims struct {
    FID    string   `json:"fid"`
    Name   string   `json:"name"`
    JTI    string   `json:"jti"`
    Scopes []string `json:"scopes,omitempty"`
    jwt.RegisteredClaims
}
```

Empty slice is permissible and means "no tool scopes granted"; the WS handlers
reject the connection in that case.

### Audit trail

Denial audit — every denied login continues to land as a JSON line in
`/var/log/edin/login-attempts.log`. The `reason` field gets new values:

- `not_on_allowlist` — unlinked + no env-var match (unchanged from today)
- `awaiting_approval` — linked but `approved=false`
- `no_scopes_granted` — linked + approved but Authentik groups map to an
  empty scope set (misconfigured group membership)
- `authentik_unreachable` — transient: Authentik API call failed or timed
  out. Deny-closed, never allow-on-error.
- `authentik_user_missing` — linked but the referenced Authentik user has
  been deleted. Distinct from `_unreachable` so admins see the actionable
  state ("re-link this commander to an existing user") vs the transient
  one.

Admin-action audit (new) — every mutation through the Commanders admin
surface appends a JSON line to `/var/log/edin/admin-actions.log`:

```json
{"time":"...","admin_sub":"auth0|...","admin_name":"david",
 "action":"commander.approve","subject_fid":"F2504","details":{...},
 "ip":"..."}
```

Retained alongside the login-attempt log; same mode 0750, same mount,
same logrotate-friendly shape. Actions logged: `commander.approve`,
`commander.deny`, `commander.link`, `commander.unlink`,
`commander.revoke_sessions` (explicit or as a side-effect of deny/unlink).

Metrics — the existing `commanderAuthAttemptsTotal{result}` gains a
complementary `commanderAccessDecisionsTotal{reason}` counter for the
finer-grained dashboard view. A latency histogram
`commanderAccessResolutionLatencySeconds` covers the Authentik API call
inside `resolveCommanderAccess`.

---

## File Map

### Create

| Path | Purpose |
|---|---|
| `internal/authz/groups.go` | Shared `ScopesForGroups(groups []string) []Scope` — single source of truth for the group-to-scope mapping |
| `internal/authz/groups_test.go` | Pin the mapping table |
| `internal/tools/scopes.go` | `toolScopes map[ToolName]authz.Scope` — per-tool required scope |
| `internal/tools/scopes_test.go` | Every defined tool has an entry; every registered tool belongs to a known scope |
| `internal/store/migrations/commander/008_commanders_authentik_link.sql` | Add `authentik_user_id UUID NULL` + `approved BOOLEAN NOT NULL DEFAULT false` + unique-partial index + RLS policy + `edin_cmd_admin` BYPASSRLS role + column-scoped GRANT |
| `internal/authentik/shadow.go` | `CreateShadowUser(ctx, fid, cmdrName) (uuid, error)` — wraps `authentik.Client.CreateUser` with the `users/edin-commanders/` path, synthetic `.invalid` email, no credentials. Idempotent on duplicate-username via fallback to `GetUserByUsername` |
| `internal/authentik/shadow_test.go` | Payload shape, duplicate-username recovery, 5xx propagation |
| `internal/httpapi/commander_linking.go` | `ensureCommanderLink(ctx, fid, cmdrName)` — callback-side helper that guarantees a link exists before resolveCommanderAccess runs |
| `internal/httpapi/commander_scopes.go` | Thin wrapper exposing `deriveCommanderScopes` from the `authz` helper in the http layer (avoids an import cycle into authz) |
| `internal/httpapi/commander_scopes_test.go` | HTTP-layer integration tests for the derivation |
| `internal/httpapi/kaine_admin_commanders.go` | Five admin endpoints: list, get-by-fid, link, unlink, approve/deny. Mutating handlers write `/var/log/edin/admin-actions.log` audit lines, require `X-Edin-Fetch: 1` header, and Deny/Unlink call `revokeAllSessions`. |
| `internal/httpapi/kaine_admin_commanders_test.go` | Handler tests with a fake `CommanderRepository`, fake `authentik.Client`, miniredis, and a tempdir audit file |
| `internal/httpapi/commander_session.go` | `revokeAllSessions(ctx, fid) error` — reads `commander:jtis:{fid}`, adds each to the revoked-jti set with TTL, clears the per-FID set. Shared helper for Deny/Unlink handlers. |
| `internal/httpapi/commander_session_test.go` | Happy-path revocation (miniredis); concurrent revoke is idempotent; already-revoked jti is a no-op |
| `internal/httpapi/csrf.go` | Shared `requireFetchHeader(w, r) bool` helper used by every mutating admin endpoint |
| `internal/httpapi/csrf_test.go` | Missing header → 400; mismatched value → 400; expected value → passthrough |
| `internal/httpapi/log_sanitize.go` | `sanitizeLogField(s string) string` — replaces `\r` and `\n` with their literal-escaped forms. Imported by the denial + admin-action log paths. |
| `internal/httpapi/log_sanitize_test.go` | CRLF-injection string is neutralized; pure ASCII passes through unchanged |
| `edin-frontend/src/pages/kaine/components/CommandersTab.jsx` | React component for the Admin → Commanders tab |
| `edin-frontend/src/pages/kaine/components/CommandersTab.test.jsx` | Vitest tests — renders list, approve toggle, link dialog, unlink |

### Modify

| Path | What changes |
|---|---|
| `internal/authz/authz.go` | Add `ScopeGalaxyRead`, `ScopeKaineMining`, `ScopeCommanderData` constants with a comment block explaining the coarse-vs-fine scope distinction |
| `internal/tools/executor.go` | Delete `kaineAllowedTools` + `copilotAllowedTools`; in `Invoke` consult `toolScopes[toolName]` after the `opsOnlyTools` check; return "not available in this context" on miss |
| `internal/tools/convert.go` | `MCPToAnthropicAll` + `BetaToolDefinitions` take a `[]authz.Scope` argument and filter via `toolScopes`; remove legacy `KaineScope` / `CopilotScope` parameters |
| `internal/tools/convert_test.go` | Add `TestConvert_FilterByKaineScopeSetMatchesLegacyKaineSet`, same for copilot — byte-for-byte parity with pre-refactor behaviour |
| `internal/auth/commander_jwt.go` | `CommanderClaims.Scopes []string`; `Issue(fid, name string, scopes []authz.Scope) (string, string, error)`; `Validate` surfaces scopes |
| `internal/auth/commander_jwt_test.go` | Round-trip scope preservation; forgery rejection; empty-scopes passes |
| `internal/httpapi/commander_middleware.go` | After validating claims, convert `claims.Scopes` (strings) → `[]authz.Scope` and call `authz.ContextWithScopes` |
| `internal/httpapi/commander_middleware_test.go` | `TestCommanderAuth_JWTScopes_InjectedToContext` |
| `internal/httpapi/commander_allowlist.go` | Rewrite core flow as `resolveCommanderAccess(ctx, fid, name) commanderAccessDecision` returning `{Allowed, Scopes, Reason, Denial}`; `enforceCommanderAllowlist` becomes a thin shim calling through |
| `internal/httpapi/commander_allowlist_test.go` | Add full decision-matrix coverage (linked+approved, linked+not-approved, unlinked+allowlist, unlinked+no-allowlist, authentik-unreachable) |
| `internal/httpapi/commander_auth.go` | `handleCommanderAuthCallback` calls `resolveCommanderAccess` once FID is known; passes returned scope set to `Issue` |
| `internal/httpapi/commander_auth_test.go` | Happy path + each denial reason → correct HTTP response and audit line |
| `internal/httpapi/commander_client_auth.go` | Desktop callback — same integration |
| `internal/httpapi/commander_client_auth_test.go` | Same test matrix |
| `internal/store/commander_repository.go` | `CommanderRow` fields `AuthentikUserID *uuid.UUID`, `Approved bool`; interface methods `SetAuthentikLink`, `SetApproved`, `ListAllCommanders`, `GetCommanderAsAdmin`; `GetCommander` returns the new fields. Internal `withAdminTx` helper that `SET LOCAL ROLE edin_cmd_admin` for cross-FID admin operations (see Task 4 RLS design). |
| `internal/store/commander_repository_test.go` | Round-trip + duplicate-link rejection + default `approved=false` + ordering + RLS isolation (writer can't read other FIDs) + admin-tx visibility |
| `internal/httpapi/kaine.go` | Extend group-prefix guard at lines ~1849/1882 to permit `edin-` in addition to `kaine-`; register the new admin routes |
| `internal/httpapi/kaine_test.go` | `edin-*` groups accepted; arbitrary groups still rejected |
| `internal/httpapi/kaine_chat.go` | Replace hardcoded `authz.ScopeKaineChat` in `handleChatMessage` ctx-scope with the set derived from `KaineUser` groups via `authz.ScopesForGroups` |
| `internal/httpapi/kaine_chat_test.go` | Tool visibility unchanged per canonical Kaine user fixtures |
| `internal/httpapi/copilot_chat.go` | `CommanderChatUser` carries the scope set; WS handler threads it into ctx; remove the hardcoded `ScopeCopilotChat` line |
| `internal/httpapi/copilot_chat_test.go` | WS session reads scopes from the nonce store's `CommanderChatUser` |
| `internal/httpapi/commander_chat_user.go` | Add `Scopes []authz.Scope` field |
| `internal/httpapi/commander_auth.go` (token endpoint) | `handleCommanderAuthToken` populates the nonce's `CommanderChatUser.Scopes` from the JWT claims |
| `internal/mcp/auth.go` | Rewrite `groupsToScopes` as a wrapper around `authz.ScopesForGroups` so MCP and commander paths share one mapping |
| `internal/authentik/client.go` | Add `CreateUser`, `GetUserByUsername`, `AddUserToGroup`, `RemoveUserFromGroup` methods + `ErrDuplicateUsername` / `ErrGroupNotFound` sentinel errors. Group name→UUID cache (populated lazily, long-lived). |
| `internal/authentik/client_test.go` | Round-trip tests per new method against httptest.Server fakes; 404 / duplicate discrimination |
| `internal/httpapi/metrics.go` | Add `commanderAccessDecisionsTotal` counter with `reason` label |
| `internal/httpapi/metrics_test.go` | Counter increments per reason |
| `internal/config/config.go` | Keep `AllowedFIDs` until the final task — document lifecycle |
| `atlas/ansible/roles/authentik/tasks/api_config.yml` | Idempotent creation of `edin-copilot` and `edin-copilot-trusted` groups, following the existing kaine-group pattern |
| `edin-backend/ansible/roles/access_list/README.md` | Ops runbook: "how to add a commander" (via AdminPage), log tailing, metric scraping |
| `edin-frontend/src/pages/kaine/AdminPage.jsx` | Add "Commanders" tab to the tab bar |
| `edin-frontend/src/pages/kaine/services/api.js` | `listCommanders`, `getCommander`, `approveCommander`, `denyCommander`, `linkCommanderToAuthentikUser`, `unlinkCommander` |

### Delete (final task only)

| Path | What / why |
|---|---|
| `internal/config/config.go` — `parseFIDAllowlist` + `AllowedFIDs` field | Env-var allowlist retired once Authentik is the only access source |
| `edin-backend/ansible/roles/access_list/defaults/main.yml` — `commander_fid_allowlist` var | Same; role persists for the log-directory side of its job |
| `edin-backend/ansible/roles/control_api/templates/control-api.env.j2` — `COMMANDER_FID_ALLOWLIST` line | Env var no longer consumed |
| `internal/httpapi/commander_allowlist.go` — `fidAllowed` function + fallback branch | Dead code once env-var path is removed |

---

## Sequential tasks

Each task ends with a **gate** — the listed test command must pass before the
task is considered complete. Every task is independently shippable to
production without breaking existing behaviour; the gating invariant is
"installing only tasks 1..N and deploying keeps every existing commander able
to log in exactly as they do today."

### Task 1 — Define new scopes and per-tool scope map

**Outcome:** New scope constants exist, a `toolScopes` registry maps every tool
to its required scope, and a `ScopesForGroups` helper exists in the `authz`
package. **No call site uses them yet.**

**Why first:** Pure additions. Builds vocabulary. Keeps the behavioural diff in
Task 2 reviewable.

**Create:**
- `internal/authz/groups.go` — `ScopesForGroups(groups []string) []Scope`
  implementing the mapping table from the Design section. Deduplicates. Ignores
  unknown groups.
- `internal/authz/groups_test.go`:
  - `TestScopesForGroups_EmptyInput_ReturnsEmpty`
  - `TestScopesForGroups_KaineGod_GrantsFullSet`
  - `TestScopesForGroups_EdinCopilot_GrantsBaseCommanderSet`
  - `TestScopesForGroups_EdinCopilotTrusted_IncludesMining`
  - `TestScopesForGroups_UnknownGroup_Ignored`
  - `TestScopesForGroups_MultipleGroups_ScopesDeduped`
  - `TestScopesForGroups_TestGroups_TreatedAsProd` — both `kaine-chat` and
    `kaine-chat-test` map to the same scopes (matches existing `HasRole`
    behaviour)
- `internal/tools/scopes.go`:
  - `var toolScopes = map[ToolName]authz.Scope{...}` — every entry documented.
    Empty `Scope{}` means "available to anyone authenticated to this product."
- `internal/tools/scopes_test.go`:
  - `TestToolScopes_EveryDefinedToolHasAnEntry` — iterate the `ToolName` const
    block; each name is a key in `toolScopes`. This is the guardrail against
    someone adding a tool and forgetting to decide its scope.
  - Per-scope grouping assertions: galaxy tools → `ScopeGalaxyRead`; mining
    tools → `ScopeKaineMining`; commander tools → `ScopeCommanderData`; ops
    tools → `ScopeLlmOperator`.

**Modify:**
- `internal/authz/authz.go`:
  - Add constants:
    ```go
    ScopeGalaxyRead     Scope = "galaxy_read"
    ScopeKaineMining    Scope = "kaine_mining"
    ScopeCommanderData  Scope = "commander_data"
    ```
  - Comment block explaining the coarse (endpoint-gate) vs fine (per-tool)
    distinction, so future readers understand why `copilot_chat` and
    `galaxy_read` both exist.

**Gate:**
```bash
cd edin-backend && go test ./internal/authz/... ./internal/tools/... -count=1
```

**Acceptance:** Full backend suite green. No runtime behaviour change.

---

### Task 2 — Scope-driven tool filtering

**Outcome:** `kaineAllowedTools` and `copilotAllowedTools` deleted. Tool
visibility everywhere flows through `toolScopes` + caller scope context. Kaine
WS handler and copilot WS handler each populate ctx with a hardcoded default
scope set that reproduces today's visibility exactly.

**Why:** Two maps-of-tools disagreeing is the bug pattern we want to make
impossible. Scope-driven filtering = one place to reason about visibility.

**Modify:**
- `internal/tools/executor.go`:
  - Delete `kaineAllowedTools` map (lines 77-107)
  - Delete `copilotAllowedTools` map (lines 111-136)
  - In `Invoke`, after the `opsOnlyTools` defense-in-depth check, do:
    ```go
    required, registered := toolScopes[toolName]
    if !registered {
        // Fail-closed: a tool not present in the scope registry is a coding
        // mistake, not an implicitly-public tool. Task 1's guardrail test
        // prevents this from reaching prod, but the runtime check is
        // defense-in-depth against a merge that ignored the test.
        return nil, fmt.Errorf("tool %q has no declared scope — refusing to invoke", name)
    }
    if required != "" && !authz.Allow(authz.ScopesFromContext(ctx), required) {
        return nil, fmt.Errorf("tool %q not available in this context", name)
    }
    ```
  - Invariant (documented in-code): **every `ToolName` in the const block
    MUST have a `toolScopes` entry**, even if the required scope is the empty
    string (explicit "no scope needed"). A missing entry is a hard error, not
    a silent "public tool." This matches the guardrail test in Task 1
    (`TestToolScopes_EveryDefinedToolHasAnEntry`) — the test prevents missing
    entries at CI time; the runtime check prevents exploitation if the test
    is somehow bypassed.
- `internal/tools/convert.go`:
  - Rework `MCPToAnthropicAll(tools []mcp.Tool, callerScopes []authz.Scope)`
    and `BetaToolDefinitions` to filter via `toolScopes`
  - Existing per-scope helpers `KaineScope`, `CopilotScope` get replaced with
    concrete scope-set constants or inlined at call sites
- `internal/tools/convert_test.go`:
  - `TestConvert_FilterByKaineScopes_MatchesLegacyKaineTools` — pin the list
    byte-for-byte against the commit immediately preceding this task. Imports
    the legacy set from a `testdata/legacy_kaine_tools.txt` file captured at
    Task 1 merge time. Locks in parity.
  - `TestConvert_FilterByCopilotScopes_MatchesLegacyCopilotTools` — same
- `internal/httpapi/kaine_chat.go`:
  - In `handleChatMessage`, replace `authz.ContextWithScopes(ctx,
    authz.ScopeKaineChat)` with a set derived from the authenticated
    `KaineUser` — e.g. `authz.ScopesForGroups(user.Groups)`.
  - Add `authz.ScopeKaineChat` explicitly to that set so the endpoint gate
    logic doesn't change (a future task could make this derivation richer).
- `internal/httpapi/copilot_chat.go`:
  - Line 234: replace `authz.ContextWithScopes(ctx, authz.ScopeCopilotChat)`
    with `authz.ContextWithScopes(ctx, session.user.Scopes...)`. The copilot
    `CommanderChatUser` type (see `commander_chat_user.go`) gains a `Scopes`
    field in this task. For now populate it with the default commander set
    (`{copilot_chat, galaxy_read, commander_data}`) at the token-issue site
    — Task 7 replaces the hardcode with JWT-derived scopes.
- `internal/httpapi/commander_chat_user.go`:
  - Add `Scopes []authz.Scope`
- `internal/httpapi/commander_auth.go` (token-nonce issue point):
  - Populate `Scopes` with the default commander set

**Gate:**
```bash
cd edin-backend && go test ./internal/tools/... ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. Ask the Kaine chat a representative question per Kaine
tool family (system lookup, plasmium buyers, BGS question). Ask the copilot a
commander-specific question and a galaxy-general question. Every answer comes
back identically to before.

---

### Task 3 — Extend `CommanderClaims` with a scopes array

**Outcome:** `CommanderClaims.Scopes []string` round-trips through JWT
issue/validate. Middleware injects the scopes into ctx via
`authz.ContextWithScopes`. Callbacks pass a scope slice to `Issue`. For this
task the scope slice is still the default commander set; Task 7 makes it
dynamic.

**Why:** JWT is the only place between login and request that can carry
per-commander scopes. Without this, every tool call needs an Authentik round
trip — far too slow and fragile.

**Modify:**
- `internal/auth/commander_jwt.go`:
  - Add `Scopes []string json:"scopes,omitempty"` to `CommanderClaims`
  - Change signature: `Issue(fid, name string, scopes []authz.Scope) (tokenString, jti string, err error)`.
    Convert scopes to strings before signing.
  - `Validate` already surfaces the struct — no code change needed beyond the
    struct definition itself.
- `internal/httpapi/commander_auth.go` & `commander_client_auth.go`:
  - After `Issue` returns, record the new `jti` under the per-FID set for
    future revocation:
    ```go
    // Key: commander:jtis:{fid}. Members: active jti values.
    // TTL on the SET itself matches the longest JWT lifetime (24h) so
    // Redis reaps the entry when all JWTs are naturally expired.
    if err := s.redis.SAdd(ctx, "commander:jtis:"+fid, jti).Err(); err != nil {
        // Best-effort: failure to record means we can't instantly-revoke
        // this particular jti via admin-triggered revocation, but the JWT
        // still expires naturally. Log and continue.
        logger.Warn("commander_jti_track_failed", "fid", fid, "err", err)
    }
    _ = s.redis.Expire(ctx, "commander:jtis:"+fid, 24*time.Hour).Err()
    ```
- `internal/httpapi/commander_auth.go` — `handleCommanderAuthLogout`:
  - Today logout adds the `jti` to the revoked set. Also `SREM
    commander:jtis:{fid} {jti}` so the per-FID tracking set stays accurate.
    This keeps `revokeAllSessions` (Task 8) from re-revoking already-expired
    or already-revoked sessions — not a correctness issue, just hygiene.
- `internal/auth/commander_jwt_test.go`:
  - `TestCommanderJWT_JTIRecordedInPerFIDSet` — issue, assert `SMEMBERS
    commander:jtis:F2504` contains the returned jti (test harness uses a
    mini-redis or the real Redis via testcontainers — follow existing
    convention in this file).
- `internal/auth/commander_jwt_test.go`:
  - `TestCommanderJWT_RoundTripPreservesScopes` — issue with
    `{ScopeGalaxyRead, ScopeCommanderData}`, validate, assert slice equality
  - `TestCommanderJWT_EmptyScopesSurvivesRoundTrip` — nil/empty passes through
  - `TestCommanderJWT_TamperedScopesRejectedBySignature` — edit the base64
    payload to add a scope, validate must fail
- `internal/httpapi/commander_middleware.go`:
  - In `withCommanderAuth`, after `s.commanderJWTValidator.Validate` returns
    successfully, convert `claims.Scopes` to `[]authz.Scope` and call
    `authz.ContextWithScopes(ctx, scopes...)` before invoking `next`.
- `internal/httpapi/commander_middleware_test.go`:
  - `TestCommanderAuth_JWTScopes_InjectedToContext` — issue a JWT with
    `{copilot_chat, commander_data}`, send a request, assert a probe handler
    sees both via `authz.ScopesFromContext`
- `internal/httpapi/commander_auth.go`:
  - At the `s.commanderJWTIssuer.Issue(fid, name)` call site (~line 325), pass
    the default commander scope set as the third argument. The scope selection
    will move into a helper in Task 7.
- `internal/httpapi/commander_client_auth.go`:
  - Same update at `Issue` call site
- `internal/httpapi/commander_auth.go` — `handleCommanderAuthRefresh`
  (third `Issue` call site, currently ~line 642):
  - Pass the same default commander scope set as the third argument
    (Task 7 will migrate this to carrying forward `claims.Scopes` from
    the old JWT once scope derivation is dynamic).
  - Refresh rotates the jti — the old jti is revoked via `RevokeJTI`
    and a new jti is minted. Keep the per-FID tracking set accurate
    by doing BOTH:
    ```go
    _ = s.redisClient.SRem(ctx, "commander:jtis:"+fid, oldJTI).Err()
    if err := s.redisClient.SAdd(ctx, "commander:jtis:"+fid, newJTI).Err(); err != nil {
        slog.Warn("commander_jti_track_failed", "fid", fid, "err", err)
    }
    _ = s.redisClient.Expire(ctx, "commander:jtis:"+fid, 24*time.Hour).Err()
    ```
    Rationale: refresh is a real mint site; without this, refresh-minted
    sessions escape `revokeAllSessions` in Task 8 (they keep working
    until natural expiry no matter how many times an admin Denies /
    Unlinks / Revokes). Security-relevant — the tracking-set invariant
    is "every active jti for a FID is enumerable via SMEMBERS."
- `internal/httpapi/commander_auth_test.go`, `commander_client_auth_test.go`:
  - Add one new test per file asserting the issued JWT's Scopes slice equals
    the default commander set.
  - Add `TestCommanderAuth_RefreshTracksNewJTIAndUntracksOld` (only in
    `commander_auth_test.go`, since refresh is web-only today):
    seed `commander:jtis:F2504` with the initial jti, call refresh,
    assert the old jti is gone from the set and the new jti is present,
    and the Expire was refreshed.

**Gate:**
```bash
cd edin-backend && go test ./internal/auth/... ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. Existing commanders keep logging in. JWT cookie now
contains `"scopes":["copilot_chat","galaxy_read","commander_data"]` — verify
by decoding the token. No user-visible change.

---

### Task 4 — Commander ↔ Authentik link schema + repository

**Outcome:** Two new columns on `commander.commanders` plus the repository
methods the admin UI and callback resolver will need in later tasks.

**Why:** Data-layer foundation. Everything downstream reads/writes these
columns.

**Create:**
- `internal/store/migrations/commander/008_commanders_authentik_link.sql`:
  ```sql
  ALTER TABLE commander.commanders
      ADD COLUMN authentik_user_id UUID NULL,
      ADD COLUMN approved          BOOLEAN NOT NULL DEFAULT false;

  -- Prevent two commanders pointing at the same Authentik user — a
  -- one-to-one link is the only shape the admin UI supports.
  CREATE UNIQUE INDEX IF NOT EXISTS idx_commanders_authentik_user_id
      ON commander.commanders(authentik_user_id)
      WHERE authentik_user_id IS NOT NULL;

  -- Row-level security on commander.commanders. Today only journal_events
  -- has RLS; extending it to commanders means a bug in the backend that
  -- mixes up FIDs cannot leak one commander's link or approval state into
  -- another commander's response. RLS kicks in on any SELECT/UPDATE
  -- performed without the admin bypass role.
  ALTER TABLE commander.commanders ENABLE ROW LEVEL SECURITY;
  ALTER TABLE commander.commanders FORCE ROW LEVEL SECURITY;

  DROP POLICY IF EXISTS commanders_self_rw ON commander.commanders;
  CREATE POLICY commanders_self_rw ON commander.commanders
      USING      (fid = current_setting('app.current_fid', true))
      WITH CHECK (fid = current_setting('app.current_fid', true));

  -- Dedicated admin role that the Kaine admin endpoints use. BYPASSRLS is
  -- scoped to the commander schema via the GRANT below — the role holds
  -- no rights outside schema `commander`, so a misuse can't read or write
  -- journal events or any other schema.
  DO $$
  BEGIN
      IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_admin') THEN
          CREATE ROLE edin_cmd_admin NOLOGIN BYPASSRLS;
      END IF;
  END $$;
  GRANT USAGE ON SCHEMA commander TO edin_cmd_admin;
  GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES    IN SCHEMA commander TO edin_cmd_admin;
  GRANT USAGE, SELECT                  ON ALL SEQUENCES IN SCHEMA commander TO edin_cmd_admin;

  -- The application connects as edin_cmd_writer; admin endpoints SET ROLE
  -- edin_cmd_admin inside a transaction when they need cross-FID reads
  -- (ListAllCommanders) or cross-FID writes (admin approve/link/unlink).
  -- cmd_writer itself remains RLS-scoped and never sees other commanders'
  -- rows.
  DO $$
  BEGIN
      IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'edin_cmd_writer') THEN
          GRANT edin_cmd_admin TO edin_cmd_writer;
          -- REVOKE the unqualified UPDATE granted in 002_commanders_table.sql
          -- BEFORE issuing the column-scoped grant. Postgres GRANT is strictly
          -- additive and cannot narrow an existing privilege — without the
          -- REVOKE the column-scoped grant is security theatre (the full-table
          -- UPDATE stays in force). This pair is what actually enforces
          -- "cmd_writer can set link/approval/last_seen/cmdr_name but never
          -- rewrite the FID." cmdr_name is in the grant list because
          -- UpsertCommander's ON CONFLICT (fid) DO UPDATE refreshes it on
          -- every login — it's not an identity field.
          REVOKE UPDATE ON commander.commanders FROM edin_cmd_writer;
          GRANT SELECT,
                UPDATE (authentik_user_id, approved, last_seen_at, cmdr_name)
             ON commander.commanders TO edin_cmd_writer;
      END IF;
  END $$;
  ```

**Modify:**
- `internal/store/commander_repository.go`:
  - `CommanderRow`: add fields
    ```go
    AuthentikUserID *uuid.UUID
    Approved        bool
    ```
  - `GetCommander`: update `SELECT` and `Scan` to include the new columns
  - Add to `CommanderRepository` interface:
    ```go
    SetAuthentikLink(ctx context.Context, fid string, userID *uuid.UUID) error
    SetApproved(ctx context.Context, fid string, approved bool) error
    ListAllCommanders(ctx context.Context) ([]CommanderRow, error)
    ```
  - `SetAuthentikLink` / `SetApproved` run under an admin-scoped transaction
    because they mutate rows the acting identity doesn't own. The repository
    grows an internal helper `withAdminTx(ctx, fn func(Tx) error) error`
    which:
    1. Begins a transaction.
    2. Runs `SET LOCAL ROLE edin_cmd_admin` to escape RLS within the tx.
    3. Invokes `fn`.
    4. `RESET ROLE` happens implicitly at tx close.
    This bypass is scoped to the admin-only mutations — all commander-self
    reads and writes continue to run as `edin_cmd_writer` with RLS enforced.
    Both methods document: "Admin-only; RLS is bypassed via edin_cmd_admin
    inside this transaction. Callers MUST already have passed withKaineAdmin."
  - `ListAllCommanders` uses the same `withAdminTx` helper — list view is
    inherently cross-FID. Returns rows ordered
    `ORDER BY last_seen_at DESC NULLS LAST, first_seen_at DESC`.
  - `GetCommander(fid)` continues to run as `edin_cmd_writer` under the
    commander's own `SET LOCAL app.current_fid = $1`, i.e. RLS enforces that
    a commander can only read their own row during the normal auth callback
    path. The admin "get by FID" handler uses a separate
    `GetCommanderAsAdmin(fid)` that goes through `withAdminTx`.

- `internal/store/commander_repository_test.go`:
  - `TestCommanderRepo_SetAuthentikLink_RoundTrip` — upsert commander, set
    link, GetCommander returns the UUID
  - `TestCommanderRepo_SetAuthentikLink_Unset_ClearsColumn` — nil pointer
    sets the column to NULL
  - `TestCommanderRepo_SetAuthentikLink_DuplicateRejected` — two commanders
    can't share one Authentik user
  - `TestCommanderRepo_SetApproved_RoundTrip`
  - `TestCommanderRepo_GetCommander_DefaultsToNotApproved`
  - `TestCommanderRepo_ListAllCommanders_OrderedByLastSeenDesc`
  - `TestCommanderRepo_RLS_WriterCannotReadOtherFIDs` — connected as
    `edin_cmd_writer` with `app.current_fid = 'F1'`, attempt to SELECT
    a commander row with `fid = 'F2'`. Expect zero rows (RLS invisibility,
    not an error).
  - `TestCommanderRepo_RLS_AdminTxSeesAllFIDs` — under `withAdminTx` the
    same SELECT returns both rows.
  - `TestCommanderRepo_SetApproved_ViaAdminTxSucceeds_ViaWriterDirectFails`
    — `SetApproved('F2')` while current_fid='F1' succeeds through the admin
    helper, fails (zero rows affected) if attempted directly via writer.

**Gate:**
```bash
cd edin-backend && go test ./internal/store/... -count=1
```
Runs under testcontainers. The existing commander_repository tests must all
still pass.

**Acceptance:** Deploy. The embedded migration runs on backend startup. On
prod, `\d commander.commanders` via `docker exec edin-timescaledb psql ...`
shows both new columns. Existing rows default to `approved=false`, `null`
link. Nothing reads the columns yet — zero runtime effect.

---

### Task 5 — Auto-link new commanders to a shadow Authentik user

**Outcome:** First-time Frontier callback for a FID whose
`commander.commanders` row has `authentik_user_id IS NULL` creates a
shadow Authentik user under `users/edin-commanders/`, links the FID to
it, and persists `approved=false` (the default). Subsequent callbacks
are no-ops on the link. Commanders are **always linked** after this
task ships.

**Why before Task 6 (resolveCommanderAccess):** making auto-link land
first means Task 6's decision matrix sees only linked rows in the
steady-state and can be simplified. Until this task ships,
resolveCommanderAccess needs the unlinked branches (allowlist fallback);
after this task, those branches exist only for the transition period
until all historical commander rows re-login at least once, then Task
12 retires them.

**Create:**
- `internal/authentik/shadow.go`:
  - `CreateShadowUser(ctx context.Context, fid, cmdrName string) (userID uuid.UUID, err error)`
    — thin wrapper around the existing `authentik.Client.CreateUser`.
    Builds the synthetic-user payload per the Design spec (path
    `users/edin-commanders`, synthetic `.invalid` email, is_active=true,
    no password, no groups). Idempotent at the call-site level: if the
    client returns `409 duplicate username`, the wrapper re-fetches the
    existing user by username and returns that UUID. This handles the
    retry case where the commander row update failed after the Authentik
    create succeeded.
  - Godoc explicitly documents: "Shadow users have no login credential.
    They exist only to hold group memberships. Do not reuse this helper
    to provision human Authentik users."

- `internal/authentik/shadow_test.go`:
  - Round-trip via a fake `CreateUser` returning a UUID; assert the
    payload has `path=users/edin-commanders`, `email` ends in
    `.invalid`, `is_active=true`, no `password` field.
  - Duplicate-username response: fake client returns 409 once, wrapper
    calls `GetUserByUsername`, returns that UUID.
  - 5xx response: wrapper propagates the error; caller (Task 6) maps
    it to a 500 to the commander.

**Modify:**
- `internal/authentik/client.go`:
  - Add `CreateUser(ctx, CreateUserRequest) (User, error)` and
    `GetUserByUsername(ctx, username string) (User, error)` methods.
    Both hit standard Authentik `POST/GET /api/v3/core/users/`. Types
    get a `Path string` field + `IsActive bool` field on the request
    struct. Error discrimination: duplicate username surfaces as
    `authentik.ErrDuplicateUsername`; user-not-found as the existing
    `authentik.ErrUserNotFound`.
- `internal/authentik/client_test.go`:
  - `TestAuthentikClient_CreateUser_Success` — happy path against a
    httptest.Server fake.
  - `TestAuthentikClient_CreateUser_Duplicate_ReturnsErrDuplicate`
  - `TestAuthentikClient_GetUserByUsername_Success`
  - `TestAuthentikClient_GetUserByUsername_404_ReturnsErrUserNotFound`

- `internal/httpapi/commander_auth.go` (`handleCommanderAuthCallback`):
  - After upserting the commander row (preserving existing first-seen /
    last-seen logic), before any access resolution, call a new helper
    `s.ensureCommanderLink(ctx, fid, cmdrName)`:
    ```go
    row, err := s.commanderRepo.GetCommanderAsAdmin(ctx, fid)  // admin-tx read for link column
    if err != nil { return 500 }
    if row.AuthentikUserID == nil {
        uuid, err := s.authentikShadow.CreateShadowUser(ctx, fid, cmdrName)
        if err != nil {
            // Deny-closed. Audit with reason=authentik_unreachable.
            s.recordDeniedLogin(..., "authentik_unreachable", ...)
            s.writeGeneric403(w); return
        }
        if err := s.commanderRepo.SetAuthentikLink(ctx, fid, &uuid); err != nil {
            // Link write failed after user created → log, but don't
            // leave commander half-linked. Next login retries.
            s.recordDeniedLogin(..., "link_persist_failed", ...)
            s.writeGeneric403(w); return
        }
    }
    // Continues into Task 6's resolveCommanderAccess...
    ```
  - Uses the admin-tx repo method (`GetCommanderAsAdmin`) because
    RLS would otherwise prevent cmd_writer reading the link column for
    the authenticating commander until `app.current_fid` is set; we
    read before we know we should — keep the admin-tx read tight.
  - The `ensureCommanderLink` helper lives in `commander_linking.go`
    (new file) alongside a shared `s.authentikShadow` dependency.

- `internal/httpapi/commander_client_auth.go`:
  - Mirror call to `ensureCommanderLink`.
- `internal/httpapi/commander_auth_test.go`, `commander_client_auth_test.go`:
  - `TestCallback_FirstLogin_CreatesShadowUserAndLinksFID` — fake
    Authentik client returns a known UUID; assert the commander row's
    `authentik_user_id` reflects it after callback completes.
  - `TestCallback_ReturningCommander_DoesNotReCreateShadow` — preseeded
    row already has the link; fake Authentik's `CreateUser` must not
    be called.
  - `TestCallback_AuthentikCreateFails_Returns500AndLeavesRowUnlinked`
  - `TestCallback_ShadowCreatedButLinkPersistFails_AuditsAndReturns403`
    — the link-persist path is the one place where Authentik state
    gets ahead of our DB; the duplicate-username handling in
    `CreateShadowUser` is what makes the next retry recover.
- `edin-backend/ansible/roles/access_list/README.md`:
  - New section "On first-login flow": "Nothing to do. The commander
    will auto-appear in the Commanders tab with `approved=false`. Use
    Grant to admit them."

**Gate:**
```bash
cd edin-backend && go test ./internal/authentik/... ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. Pre-deploy, delete your commander row's
`authentik_user_id` via an admin SQL console to simulate a new
commander:
```sql
UPDATE commander.commanders SET authentik_user_id = NULL WHERE fid = 'F2504';
```
Log in via copilot. Expect:
- The Frontier callback completes
- A new Authentik user appears in `users/edin-commanders/` (verify via
  Authentik admin UI)
- Your commander row has `authentik_user_id` set, `approved=false`
- `resolveCommanderAccess` (still pre-Task 6 semantics) returns you to
  the env-var allowlist branch → allowed
- You can also **not** have been re-created again on a subsequent
  login (shadow stays stable)

---

### Task 6 — `resolveCommanderAccess` + scope derivation integration

**Outcome:** Single decision-point helper that inputs an FID and outputs
`{Allowed, Scopes, Reason, Denial}`. Both callbacks delegate to it (after
`ensureCommanderLink` from Task 5 has run, so every row is linked).

**Why:** One place to look at. "Did this commander get in, and why?" is answered
by reading one function and one log line.

**Post-Task-5 invariant:** Task 5's `ensureCommanderLink` runs before
`resolveCommanderAccess` on every callback, so the "row absent" and
"linked=false" branches in the decision matrix below exist only for the
transition window where a commander row pre-existed without an
`authentik_user_id` (possible if this task deploys before every historical
commander has logged in post-Task-5). Task 12 retires them along with
the env-var fallback.

**Modify:**
- `internal/httpapi/commander_allowlist.go`:
  - Introduce:
    ```go
    type commanderAccessDecision struct {
        Allowed bool
        Scopes  []authz.Scope
        Reason  string          // labels used in metrics + denial log
        Denial  *deniedLoginAttempt  // populated only when Allowed=false
    }

    func (s *Server) resolveCommanderAccess(
        ctx context.Context, r *http.Request, flow loginAttemptAuthFlow,
        fid, name string,
    ) commanderAccessDecision
    ```
  - Decision matrix (documented inline):
    | Commander row (cmd_writer lookup) | Linked + approved? | Allowlist hit? | Return |
    |---|---|---|---|
    | row present | Yes | — | `allowed=true`, scopes = `authz.ScopesForGroups(groups)`, reason=`authentik_groups` |
    | row present | Yes, but Authentik user deleted (404) | — | `allowed=false`, reason=`authentik_user_missing` |
    | row present | Yes, but Authentik call transiently fails | — | `allowed=false`, reason=`authentik_unreachable` |
    | row present | Yes, but `ScopesForGroups` empty | — | `allowed=false`, reason=`no_scopes_granted` |
    | row present | No + on allowlist | — | `allowed=true`, scopes = default commander set, reason=`allowlist_fallback` |
    | row present | No + not on allowlist, linked=true | — | `allowed=false`, reason=`awaiting_approval` |
    | row present | No + not on allowlist, linked=false | — | `allowed=false`, reason=`not_on_allowlist` |
    | row absent | N/A | Yes | `allowed=true`, scopes = default commander set, reason=`allowlist` |
    | row absent | N/A | No | `allowed=false`, reason=`not_on_allowlist` |
  - Authentik groups fetch via `s.authentikClient.GetUser(ctx, *row.AuthentikUserID)` with a 2s timeout. Error handling is discriminated:
    - `404 Not Found` (`errors.Is(err, authentik.ErrUserNotFound)`) → the
      linked Authentik user has been deleted out from under us. Return
      `allowed=false`, reason=`authentik_user_missing`. Admin UI surfaces
      this as a broken link with a "re-link or unlink" prompt. **Different
      reason code from transient failure** so admins see the actionable
      state rather than a spurious "Authentik is down" alert.
    - Any other error (timeout, 5xx, network) → reason=`authentik_unreachable`,
      deny-closed.
  - Preserves the existing `enforceCommanderAllowlist` function as a thin shim
    that calls the resolver and writes the 403 + log on denial. Tests against
    it continue to work.

- `internal/httpapi/commander_allowlist_test.go`:
  - Replace the current set of tests with the decision-matrix enumeration:
    - `TestResolveCommanderAccess_LinkedApproved_UsesAuthentikGroups`
    - `TestResolveCommanderAccess_LinkedApproved_NoGroupsMapped_Denied`
    - `TestResolveCommanderAccess_LinkedNotApproved_OnAllowlist_UsesFallback`
    - `TestResolveCommanderAccess_LinkedNotApproved_OffAllowlist_DeniesAwaiting`
    - `TestResolveCommanderAccess_UnlinkedOnAllowlist_UsesFallback`
    - `TestResolveCommanderAccess_UnlinkedOffAllowlist_Denied`
    - `TestResolveCommanderAccess_AuthentikUnreachable_DeniesClosed`
    - `TestResolveCommanderAccess_AuthentikUserDeleted_DeniesWithMissingReason`
      — fake client returns `authentik.ErrUserNotFound`; decision has
      `reason=authentik_user_missing`, not `authentik_unreachable`.
  - Test harness needs a fake `authentik.Client` — stub struct implementing the
    subset of methods `resolveCommanderAccess` consults.

- `internal/httpapi/commander_auth.go`:
  - After FID is known, replace `s.enforceCommanderAllowlist(...)` with a call
    to `resolveCommanderAccess`. Forward the decision to `Issue` for scopes.
  - On denial, call `recordDeniedLogin(decision.Denial)` and return 403 (as
    the current enforceCommanderAllowlist does).
- `internal/httpapi/commander_client_auth.go`:
  - Mirror update.
- `internal/httpapi/commander_auth_test.go`, `commander_client_auth_test.go`:
  - For each callback, cover one representative allow path (issues JWT with
    expected scopes) and one denial path (returns 403 + writes audit line).
- `internal/httpapi/metrics.go`:
  - New counter `commanderAccessDecisionsTotal` with label `reason`.
  - `resolveCommanderAccess` increments on every return.

**Gate:**
```bash
cd edin-backend && go test ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. Currently no commander is linked → all logins take the
`not_on_allowlist` or `allowlist` paths (existing behaviour). Metrics scrape
shows `reason=allowlist` incrementing on your logins.

---

### Task 7 — Nonce store + WS threading of scopes

**Outcome:** The copilot WS session carries the scope set from the JWT through
to the tool-invocation context. Task 2 populated the nonce store's
`CommanderChatUser.Scopes` with a hardcoded default; this task swaps it for
the real scopes from the callback's decision.

**Why:** Closes the loop from "callback derives scopes" to "tool invocation
checks scopes." Without this, scope work done in Tasks 3+5 is invisible to the
runtime.

**Modify:**
- `internal/httpapi/commander_auth.go` (`handleCommanderAuthToken`):
  - When issuing the nonce, populate `CommanderChatUser.Scopes` from
    `claims.Scopes` (the JWT we just validated to get here).
- `internal/httpapi/copilot_chat.go` (`handleCopilotChatWebSocket`):
  - After consuming the nonce and building the session, thread
    `session.user.Scopes` into the per-iteration ctx in `handleCopilotMessage`:
    ```go
    ctx = authz.ContextWithScopes(ctx, session.user.Scopes...)
    ```
    (replaces the hardcoded single-scope line from Task 2)
- `internal/httpapi/copilot_chat_test.go`:
  - New test: `TestCopilotWS_ScopesFromNonceReachToolContext` — issue nonce
    for a `CommanderChatUser` with `{galaxy_read, commander_data}`, open WS,
    verify a probe tool sees those scopes in ctx
- `internal/httpapi/commander_auth_test.go`:
  - `TestCommanderAuthToken_NoncePayloadMirrorsJWTScopes`

**Gate:**
```bash
cd edin-backend && go test ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. Log in. Open copilot chat. Ask about your commander
events — works (ctx has `commander_data`). Ask about plasmium buyers —
**refused with "tool not available in this context"** (ctx lacks
`kaine_mining`). This is the first visible behavioural difference from the
old hardcoded copilot tool set — and it's the correct one. **Confirm before
shipping** by comparing the Task 2 parity test output; the set should be
identical.

---

### Task 8 — Admin API: commander management endpoints

**Outcome:** Admin endpoints behind `withKaineAuth + withKaineAdmin` that
let admins list commanders and run two primary one-click actions —
**Grant** (approve + add group) and **Revoke** (deny + remove all
managed groups + revokeAllSessions) — plus Link/Unlink as break-glass
tools for the rare shadow↔real-account merge case.

**Why:** Human UI backend. Without it, Task 10 is just a page hitting 404s.
The Grant/Revoke composition is what makes the admin flow "one click per
commander" instead of "flip approve, navigate to Users tab, find shadow
user, add group, hope you got it right."

**Create:**
- `internal/httpapi/kaine_admin_commanders.go`:
  - `handleKaineAdminCommanders` — `GET /api/kaine/admin/commanders`:
    returns `[{fid, cmdr_name, approved, authentik_user_id, authentik_username, authentik_user_present, capi_link_pending, first_seen_at, last_seen_at}]`.
    `authentik_username` requires a GetUser call per linked commander; small
    N (single-admin deployment), so N+1 is acceptable. On GetUser 404 the
    handler sets `authentik_user_present=false` so the UI renders a "broken
    link" state. `capi_link_pending` reflects whether the commander has a
    CAPI refresh token stored (future-admin-action cue; sourced from the
    existing capi_tokens table's EXISTS check).
  - `handleKaineAdminCommanderByFID` — `GET /api/kaine/admin/commanders/{fid}`:
    same shape as a list entry, plus the commander's Authentik group names
    (so the UI can display "in edin-copilot") and last_denial_reason (from
    the most recent login-attempts.log entry within the last 30d — a simple
    Redis-cached pointer is enough, not a hot-path lookup).
  - `handleKaineAdminCommanderGrant` — `POST /api/kaine/admin/commanders/{fid}/grant`
    body `{"group": "edin-copilot"}` (or `"edin-copilot-trusted"`). Primary
    admin action. Inside a single handler, atomically:
    1. Validate `group` ∈ {`edin-copilot`, `edin-copilot-trusted`}. Any
       other group → 400. This is the allow-list of groups grantable via
       this endpoint; it is **not** the same allow-list as the generic
       kaine-admin user-group endpoint (Task 8 route still allows
       `kaine-*` / `edin-*` generally). Grant is narrower by design so
       slips can't accidentally give a commander `kaine-admin` rights.
    2. Look up the commander row (admin-tx). Require `authentik_user_id
       IS NOT NULL` — if it's null the commander never completed
       auto-link (Task 5 prerequisite), so this is a 409
       `commander_not_linked`. Should only happen during the transition
       window before Task 5 covers every commander.
    3. Call `authentik.Client.AddUserToGroup(ctx, userID, groupName)`.
    4. `SetApproved(fid, true)` via admin-tx.
    5. Audit line: `action=commander.grant`, `details={"group":
       "edin-copilot"}`.

    If step 3 fails after step 4 has already succeeded — not possible
    here because step 4 comes after step 3, deliberately. If step 4
    fails after step 3 succeeded, the group has been added but
    `approved=false`; this is safe (no access granted) but visible in
    the audit log; admin can retry. If step 3 fails, nothing visible
    has changed.

  - `handleKaineAdminCommanderRevoke` — `POST /api/kaine/admin/commanders/{fid}/revoke`.
    Primary admin action. Inside a single handler:
    1. Look up commander row (admin-tx). If no linked Authentik user,
       still run steps 2+4 (approved flip + session revocation).
    2. `SetApproved(fid, false)` via admin-tx.
    3. For each group in {`edin-copilot`, `edin-copilot-trusted`},
       call `authentik.Client.RemoveUserFromGroup(ctx, userID, group)`.
       Ignore 404 (not a member). This is the "remove all managed
       groups" step — the Revoke endpoint deliberately does NOT touch
       `kaine-*` groups (those are managed via the Users tab, not
       mixed into commander revocation).
    4. `revokeAllSessions(ctx, fid)` — any live JWT is cut now.
    5. Audit line: `action=commander.revoke`, `details={"groups_removed":
       ["edin-copilot"]}`.

    Order matters: approved-flip → group-removal → session-revocation.
    If any step fails partway, the earlier steps remain applied — that's
    fine because they're each "tighter access" and we're failing closed.

  - `handleKaineAdminCommanderLink` — `POST /api/kaine/admin/commanders/{fid}/link`
    body `{"authentik_user_id": "<uuid>"}`. **Break-glass only** —
    auto-link (Task 5) means every commander already has a link. This
    endpoint exists to let an admin re-link a FID from its auto-created
    shadow to an existing real Authentik user (the David case).
    Validates the Authentik user exists (`GetUser` returns 404 → 400
    "authentik user does not exist"). On unique-index violation from
    the DB (another commander already links to this user) → map
    `pgconn.PgError.Code == "23505"` to **409 Conflict** with body
    `{"error":"authentik_user_already_linked","conflicting_fid":"F2504"}`.
    The UI shows an inline warning with the conflicting FID. After a
    successful re-link, also call `revokeAllSessions(ctx, fid)` — the
    previous JWT's scope derivation was against the old linked user,
    so we force re-auth.
  - `handleKaineAdminCommanderUnlink` — `POST /api/kaine/admin/commanders/{fid}/unlink`.
    **Break-glass only** — under normal operation the auto-link is the
    link. Unlinking sets `authentik_user_id=NULL`. Also calls
    `revokeAllSessions(ctx, fid)` and flips `approved=false`. Next
    Frontier login re-triggers auto-link (Task 5) — so this doesn't
    permanently lock the commander out; it resets the link state.
  - `handleKaineAdminCommanderApprove` / `handleKaineAdminCommanderDeny` —
    low-level endpoints that flip only the `approved` boolean,
    **without** touching group membership. Kept for completeness and
    for the rare case where the admin wants to gate a commander without
    changing their group state (e.g. a temporary lockout). UI primarily
    surfaces Grant/Revoke; Approve/Deny are available under an "Advanced"
    affordance. **Deny additionally calls `revokeAllSessions(ctx, fid)`.**
    Approve does *not* revoke.
  - Shared helper `revokeAllSessions(ctx context.Context, fid string) error`:
    ```go
    // 1. SMEMBERS commander:jtis:{fid}
    // 2. For each jti, SADD commander:revoked-jtis {jti} with TTL = max
    //    remaining JWT lifetime (24h — safe over-estimate).
    // 3. DEL commander:jtis:{fid}
    // Next request carrying any of those JWTs hits the validator's
    // revoked-jti lookup and returns 401.
    ```
    Sits in `commander_session.go` (new small file) next to the existing
    revoked-jti machinery so both read/write the same Redis keys.
  - Every mutating call writes TWO audit records:
    1. **Text log (operator tailing):** `logger.Info` with sanitized display
       name + acting admin sub + subject FID + action.
    2. **Structured admin-action file:** one JSON line appended to
       `/var/log/edin/admin-actions.log`, shape per the Design section
       (time, admin_sub, admin_name, action, subject_fid, details, ip).
       Same file-handling pattern as the denial log (bind-mounted from the
       host so writes survive container rebuilds).
  - **CSRF defense.** Every mutating handler requires the header
    `X-Edin-Fetch: 1` — a value that browsers will not attach on a
    cross-site form POST. Missing / mismatched → 400. Documented as a
    shared helper `requireFetchHeader(w, r) bool` reused by the whole
    admin surface (including the existing user-group endpoints, which
    get a small compat update to require it too). Double layer on top of
    SameSite=Lax cookies, which the existing kaine cookie already sets.

- `internal/httpapi/kaine_admin_commanders_test.go`:
  - Permission gate via missing admin role → 403
  - Missing `X-Edin-Fetch: 1` header → 400 (CSRF defense)
  - Invalid `{fid}` path (e.g. empty, path-traversal) → 400
  - Grant with unknown group → 400
  - Grant with `kaine-admin` or any non-edin-copilot group → 400 (narrow
    allow-list is enforced)
  - Grant on unlinked commander → 409 `commander_not_linked`
  - Grant happy path: calls `AddUserToGroup` AND flips approved=true AND
    writes audit line with `action=commander.grant`
  - Grant when `AddUserToGroup` fails → approved stays false, no audit
    written, 500 to caller
  - Revoke happy path: calls `RemoveUserFromGroup` for both managed
    groups, flips approved=false, calls `revokeAllSessions`, audits
  - Revoke idempotency: running Revoke twice doesn't error when the
    second call sees a commander already not in the groups (404 ignored)
  - Revoke with no prior link still runs approved+revokeAllSessions
    (regression guard for break-glass case)
  - Missing target Authentik user on Link → 400 with clear message
  - Duplicate Link (link commander A to user X, then try commander B → user
    X) → 409 with `conflicting_fid` in the response body
  - Re-link calls revokeAllSessions (regression guard)
  - Deleted Authentik user surfaces as `authentik_user_present=false` in
    GET responses (and does NOT crash the list endpoint)
  - Link + list reflects link
  - Approve + list reflects approval (advanced endpoint)
  - Unlink clears the link AND calls revokeAllSessions AND sets approved=false
  - Deny flips approved AND calls revokeAllSessions
  - Approve does **not** call revokeAllSessions (regression guard — scope
    addition should not kick users out)
  - Admin-action audit file receives one line per mutation with the
    expected shape (separate lines per action; Grant emits one
    `commander.grant` and one `commander.revoke_sessions` side-effect
    line is **not** written — Grant never revokes)
  - Admin-action audit log does not leak newlines injected via display name
    (sanitizeLogField is exercised)
  - Each test spins the HTTP handler with fake `CommanderRepository`, fake
    `authentik.Client`, miniredis for the session-revocation machinery, and
    a tempdir for the admin-action audit file

**Modify:**
- `internal/authentik/client.go`:
  - Add `AddUserToGroup(ctx, userID uuid.UUID, groupName string) error`
    and `RemoveUserFromGroup(ctx, userID uuid.UUID, groupName string) error`.
    Both map to Authentik's group-members endpoints (`POST /api/v3/core/groups/{pk}/add_user/` and `/remove_user/`). Lookup
    group by name → group UUID on the first call; cache the name→UUID
    map for the duration of the client instance (it's a tiny table and
    doesn't change at runtime). Error discrimination: group not found →
    `authentik.ErrGroupNotFound`; user not a member on remove → 404,
    map to nil (idempotent).
- `internal/authentik/client_test.go`:
  - `TestAuthentikClient_AddUserToGroup_Success`
  - `TestAuthentikClient_AddUserToGroup_GroupNotFound_ReturnsErrGroupNotFound`
  - `TestAuthentikClient_RemoveUserFromGroup_Success`
  - `TestAuthentikClient_RemoveUserFromGroup_NotAMember_Idempotent` —
    returns nil even when Authentik says 404
- `internal/httpapi/kaine.go`:
  - Route registration (join the existing `/api/kaine/admin/*` block):
    ```go
    mux.HandleFunc("/api/kaine/admin/commanders",
        s.withKaineAuth(s.withKaineAdmin(s.handleKaineAdminCommanders)))
    mux.HandleFunc("/api/kaine/admin/commanders/",
        s.withKaineAuth(s.withKaineAdmin(s.handleKaineAdminCommanderByPath)))
    ```
    `handleKaineAdminCommanderByPath` is a subtree dispatcher — parses the
    path, routes to
    `grant`/`revoke`/`link`/`unlink`/`approve`/`deny`/`byFID` sub-handlers.
  - At lines ~1849 and ~1882 (group-prefix guard in the existing
    `handleKaineAdminUserByID` group-add/remove endpoint):
    ```go
    if !strings.HasPrefix(input.Group, "kaine-") && !strings.HasPrefix(input.Group, "edin-") {
        s.writeError(w, http.StatusBadRequest, "can only manage kaine-* or edin-* groups")
        return
    }
    ```
- `internal/httpapi/kaine_test.go`:
  - `TestKaineAdminUserByID_AddEdinGroup_Allowed`
  - `TestKaineAdminUserByID_RemoveEdinGroup_Allowed`
  - `TestKaineAdminUserByID_AddArbitraryGroup_StillRejected` — regression guard

**Gate:**
```bash
cd edin-backend && go test ./internal/httpapi/... -count=1
```

**Acceptance:** Deploy. As your admin Kaine cookie, `curl
https://edin.space/api/kaine/admin/commanders` returns a list with your FID
in it (auto-linked from Task 5). Issue:
```bash
curl -X POST -H 'X-Edin-Fetch: 1' -H 'Content-Type: application/json' \
     --cookie "$KAINE_COOKIE" \
     -d '{"group":"edin-copilot"}' \
     https://edin.space/api/kaine/admin/commanders/F2504/grant
```
Expect 200. Check: Authentik UI shows your shadow user in `edin-copilot`;
commander row has `approved=true`. Log out of copilot, log back in — scopes
now come from Authentik group membership. Decode the JWT to verify. **You
are now auth'd purely via Authentik;** the env-var fallback was not
consulted. Issue a Revoke:
```bash
curl -X POST -H 'X-Edin-Fetch: 1' --cookie "$KAINE_COOKIE" \
     https://edin.space/api/kaine/admin/commanders/F2504/revoke
```
Expect 200. Immediately next copilot request: 401 (JWT revoked). Next
copilot login attempt: 403 `awaiting_approval`.

---

### Task 9 — Authentik groups: provision `edin-copilot` and `edin-copilot-trusted`

**Outcome:** Ansible creates both groups idempotently. Ops doesn't need to
click around in the Authentik UI to provision.

**Why:** Without these groups, Task 10's Grant flow has nothing to add
commanders to (`reason=no_scopes_granted` on any approved login). Ansible
means new environments work out of the box.

**Modify:**
- `atlas/ansible/roles/authentik/tasks/api_config.yml`:
  - Add a new block after the existing `kaine-*` group creation tasks, using
    the same `get → check → create-if-missing` pattern:
    ```yaml
    - name: Check if edin-copilot group exists
      ansible.builtin.uri:
        url: "http://localhost:{{ authentik_http_port }}/api/v3/core/groups/?name=edin-copilot"
        method: GET
        headers: { Authorization: "Bearer {{ authentik_api_token }}" }
        status_code: 200
      register: edin_copilot_check
      tags: [authentik, api, edin]
    - name: Create edin-copilot group
      when: edin_copilot_check.json.results | length == 0
      ansible.builtin.uri:
        url: "http://localhost:{{ authentik_http_port }}/api/v3/core/groups/"
        method: POST
        headers: { Authorization: "Bearer {{ authentik_api_token }}" }
        body_format: json
        body: { name: "edin-copilot" }
        status_code: 201
      tags: [authentik, api, edin]
    ```
  - Same pair for `edin-copilot-trusted`.

**Gate:** Ansible role has no unit tests; operational gate:
```bash
make deploy-atlas  # or the narrower atlas-authentik target
ssh -p 2222 debian@51.178.89.95 'sudo docker exec authentik-server ak shell -c "from authentik.core.models import Group; print(sorted([g.name for g in Group.objects.filter(name__startswith=\"edin-\")]))"'
```
Expected output: `['edin-copilot', 'edin-copilot-trusted']`. Re-running the
deploy should show `changed=0` for both group-creation tasks (idempotency).

**Acceptance:** In the Authentik admin UI under Directory → Groups, both
groups appear. Assigning your user to `edin-copilot` via the Kaine AdminPage
(Task 8 already wired) surfaces as expected on next login.

---

### Task 10 — Admin UI: Commanders tab

**Outcome:** New tab in `AdminPage.jsx`. Lists commanders with action buttons.
Approvals, links, unlinks all happen here, with confirmation dialogs on
destructive actions.

**Why:** Day-to-day ops. Without it, Task 8's endpoints are curl-only.

**Create:**
- `edin-frontend/src/pages/kaine/components/CommandersTab.jsx`:
  - List view columns: FID, commander name, state (one of:
    "Awaiting approval" / "Active (edin-copilot)" / "Active (trusted)" /
    "Revoked" / "Broken link"), last seen (relative time).
  - Row **primary actions** (depend on current state):
    - `Awaiting approval` row → two buttons: **"Grant edin-copilot"**
      (primary), **"Grant trusted"** (secondary). One click. Optimistic
      update with revert on error. Success toast: "Granted. {FID} can now
      log in."
    - `Active` row → single button **"Revoke"**. Confirmation dialog:
      "Revoke {FID}? This removes them from the edin-copilot group,
      invalidates their current session, and blocks new logins until
      regranted." Optimistic update; success toast.
    - `Active (edin-copilot)` row also shows a subtle "Upgrade to trusted"
      link that runs Revoke → Grant(trusted) as two chained calls, with
      a single confirmation.
    - `Broken link` row (authentik_user_present=false) → primary action is
      **"Re-link"** which opens the Link modal; secondary is **"Unlink"**.
    - `Revoked` row → primary action **"Grant edin-copilot"** to re-admit.
  - **Self-action guard.** Any Revoke / Deny / Unlink targeting your own
    FID (compared against the kaine user's linked_fid if set, else against
    the authenticated kaine user's email matching the shadow's email — the
    UI has enough signal for this) pops an extra confirmation dialog
    titled "You're about to revoke your own access" before submitting.
    Prevents the sole admin from accidentally locking themselves out.
  - **Advanced pane** (collapsed by default under a "…" menu or an
    "Advanced" disclosure): Approve toggle, Deny toggle, Link user modal,
    Unlink button. These remain available for non-standard cases but are
    not the primary path.
  - Link modal: typeahead fetches `/api/kaine/admin/users` (existing
    endpoint). Submit posts to `/link`. On 409 response: the modal stays
    open and shows the warning "This Authentik user is already linked to
    {conflicting_fid}. Unlink it there first."
  - All mutating calls attach header `X-Edin-Fetch: 1` via the shared
    `apiRequest` helper (modified once, applies everywhere).
  - Follows the existing tab's visual vocabulary: Tailwind kaine-* palette,
    Lucide icons, `motion` for state transitions. Mirrors `AdminPage`'s
    `UsersTab` to the extent possible.

- `edin-frontend/src/pages/kaine/components/CommandersTab.test.jsx`:
  - `renders commander list from listCommanders()` — vitest + MSW (or
    fetch-mock) stubs `/api/kaine/admin/commanders`
  - `awaiting-approval row shows Grant edin-copilot + Grant trusted buttons`
  - `Grant edin-copilot calls grantCommander(fid, "edin-copilot") and
    re-fetches list`
  - `Grant trusted calls grantCommander(fid, "edin-copilot-trusted")`
  - `Revoke shows confirmation then calls revokeCommander(fid)`
  - `Revoke on own FID shows additional self-lockout warning dialog`
  - `upgrade to trusted runs revoke then grant(trusted) with one
    confirmation`
  - `advanced pane is collapsed by default; expanding reveals Approve /
    Deny / Link / Unlink`
  - `link dialog typeahead shows Authentik users matching the query`
  - `link submit calls linkCommanderToAuthentikUser(fid, userId) and closes
    the dialog`
  - `link 409 response keeps dialog open, surfaces conflicting fid`
  - `deny on self fid shows a confirmation dialog before submitting`
  - `unlink confirmation then POST then list refetch`
  - `broken link row renders "Re-link / Unlink" CTA when
    authentik_user_present=false`

**Modify:**
- `edin-frontend/src/pages/kaine/AdminPage.jsx`:
  - Add a third tab button between "Users" and "System Prompt": "Commanders"
  - Import `CommandersTab` and render it when that tab is active

- `edin-frontend/src/pages/kaine/services/api.js`:
  - Add:
    ```js
    export async function listCommanders() {
        return apiRequest('/admin/commanders');
    }
    export async function getCommander(fid) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}`);
    }
    // Primary admin actions: Grant bundles approved=true + group-add,
    // Revoke bundles approved=false + group-remove + session revoke.
    export async function grantCommander(fid, group) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/grant`, {
            method: 'POST',
            body: JSON.stringify({ group }),
        });
    }
    export async function revokeCommander(fid) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/revoke`, { method: 'POST' });
    }
    // Break-glass / advanced:
    export async function linkCommanderToAuthentikUser(fid, authentikUserId) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/link`, {
            method: 'POST',
            body: JSON.stringify({ authentik_user_id: authentikUserId }),
        });
    }
    export async function unlinkCommander(fid) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/unlink`, { method: 'POST' });
    }
    export async function approveCommander(fid) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/approve`, { method: 'POST' });
    }
    export async function denyCommander(fid) {
        return apiRequest(`/admin/commanders/${encodeURIComponent(fid)}/deny`, { method: 'POST' });
    }
    ```
  - All via the existing cookie-auth `apiRequest` helper (handles 401 →
    redirect as it does for other kaine calls). `apiRequest` itself gets a
    one-line change: every non-GET request sets `X-Edin-Fetch: 1` to
    satisfy the backend's CSRF guard. Applies to every mutating admin call,
    not just commanders. Existing endpoints (user group add/remove) expect
    it after Task 8.

**Gate:**
```bash
cd edin-frontend && npm run test:run
```
Full frontend suite passes; new tests exercise render + three user actions.

**Acceptance:** Deploy. Load `https://edin.space/kaine/admin`. Click
"Commanders". See yourself + any other commanders who've logged in. Your
row (assuming Task 5 has shipped) is already linked to your auto-created
shadow Authentik user. Click **Grant edin-copilot** on your row. Log out
of copilot and log back in — scopes now come from Authentik group
membership. Verify via JWT decode or via the metrics counter
`commander_access_decisions_total{reason="authentik_groups"}` ticking up.
Click **Revoke**; your next copilot request returns 401 within the same
second.

---

### Task 11 — Observability and runbook

**Outcome:** Metrics and documentation so "who's in, who's been denied, how do
I add someone" are discoverable without reading code.

**Why:** If the only way to know the system is to read Go, we've built
technical debt.

**Modify:**
- `internal/httpapi/metrics.go`:
  - The `commanderAccessDecisionsTotal` counter was added in Task 6. This
    task adds histogram `commanderAccessResolutionLatencySeconds` covering
    the Authentik call inside `resolveCommanderAccess` — so we can spot
    Authentik slowdowns before they become denials.
- `internal/httpapi/metrics_test.go`:
  - Assertions: after one allow path and one deny path, both the counter and
    histogram have observed samples.
- `edin-backend/ansible/roles/access_list/README.md`:
  - Section "Adding a new commander":
    1. Have them log in once via copilot — this auto-creates a shadow
       Authentik user under `users/edin-commanders/`, links it to their
       FID, and adds a `commander.commanders` row with `approved=false`.
       They'll see a generic 403 on this first login — that's expected.
    2. Admin visits Kaine AdminPage → Commanders → finds the "Awaiting
       approval" row.
    3. Click **Grant edin-copilot** (or **Grant trusted** for mining
       access). One click — this sets `approved=true` AND adds the chosen
       group in Authentik, in a single atomic action.
    4. They log out + log in — scopes now apply.
    Total operator work: one click per commander after they log in once.
    No more navigating to the Users tab, no more account creation.
  - Section "Removing a commander":
    1. Click **Revoke** on their Commanders row.
    2. Confirm the dialog.
    3. Effective immediately — any live JWT is invalidated; their next
       copilot request returns 401; their next login returns 403.
  - Section "Tailing the denial log":
    ```bash
    ssh -p 2222 debian@51.178.89.95 'sudo tail -f /var/log/edin/login-attempts.log'
    ```
  - Section "Tailing the admin-actions audit":
    ```bash
    ssh -p 2222 debian@51.178.89.95 'sudo tail -f /var/log/edin/admin-actions.log'
    ```
    Every approve/deny/link/unlink/revoke goes here. This is the canonical
    "who changed what access" record — always check this first when
    investigating a scope-related incident. JSON-per-line, safe to pipe to
    `jq`.
  - Section "Revoking access": toggle Deny on the Commanders row, or click
    "Unlink". Both actions are effective **immediately** — backend revokes
    any active JWTs via `revokeAllSessions` before returning 200. Confirm by
    watching the admin-actions log for the matching `commander.revoke_sessions`
    entry.
  - Section "Metrics of interest":
    - `commander_access_decisions_total{reason="authentik_unreachable"}` —
      spike indicates Authentik incident
    - `commander_access_decisions_total{reason="awaiting_approval"}` —
      volume of pending approvals
    - `commander_access_resolution_latency_seconds` p95 — SLO on callback
      latency

**Gate:**
```bash
cd edin-backend && go test ./internal/httpapi/... -count=1
```

**Acceptance:** `/metrics` scrape shows populated counter + histogram after
traffic. README reads cleanly to a newcomer.

---

### Task 12 — Retire the env-var allowlist

**Outcome:** `COMMANDER_FID_ALLOWLIST` disappears from config, env template,
Ansible, and code. The access_list Ansible role remains for its other job
(owning the login-attempt + admin-action log directory), but its FID list
is gone.

**Why:** A live fallback is tech debt once the primary path is proven.

**Deploy ordering note (applies to every task in this plan, reiterated here
because this one is the one-way door):** backend **always** deploys before
frontend. The backend is responsible for the contract (routes, response
shapes, header requirements); the frontend is its consumer. Deploying a
frontend that expects a route the backend hasn't shipped yields user-facing
404s. The reverse — backend ships first, frontend still calls the old
shape — just means unused new endpoints sitting idle, which is fine. The
Makefile's `make deploy-all` target already enforces this order; manual
deploys must replicate it. Task 12 is the first task where forgetting this
ordering causes a concrete outage: if the allowlist env var disappears
from the backend before the frontend is able to show "awaiting approval"
state, legitimate unlinked commanders see a bare 403 page with no path
forward. So: **deploy backend, verify a legitimate login succeeds, then
deploy frontend.**

**Preconditions (must be true before starting):**

1. At least **2 weeks** have passed since Task 10 shipped, observed via
   `commander_access_decisions_total{reason="authentik_groups"} > 0` growth
   rate indicating commanders are actively using the Authentik path.
2. `commander_access_decisions_total{reason="allowlist"}` has flattened —
   no new logins are taking the fallback path.
3. Every FID currently in `commander_fid_allowlist` in the access_list role
   corresponds to a commander row with `approved=true AND authentik_user_id
   IS NOT NULL`. Query:
   ```sql
   SELECT fid, approved, authentik_user_id FROM commander.commanders
    WHERE fid = ANY($1);  -- $1 = allowlist array
   ```
   Any row not-yet-linked or not-yet-approved must be resolved first
   (either migrate them to the Authentik path or remove them from the
   allowlist deliberately).

**Modify:**
- `internal/config/config.go`:
  - Remove `AllowedFIDs` field from `CommanderAuthConfig`
  - Remove `parseFIDAllowlist` function
  - Remove the `AllowedFIDs: parseFIDAllowlist(...)` line in `loadCommanderAuthConfig`
- `internal/config/config_test.go`:
  - Remove tests for `parseFIDAllowlist`
- `internal/httpapi/commander_allowlist.go`:
  - Delete `fidAllowed` function
  - In `resolveCommanderAccess`: remove the allowlist branches. An unlinked
    commander is now always denied with `reason=not_on_allowlist` (the reason
    string stays for log continuity, but the meaning is narrower: "no
    Authentik link exists"). A linked-but-not-approved commander is denied
    with `reason=awaiting_approval`.
- `internal/httpapi/commander_allowlist_test.go`:
  - Remove the allowlist-fallback tests
  - Replace with: `TestResolveCommanderAccess_UnlinkedCommander_AlwaysDenied`
- `edin-backend/ansible/roles/access_list/defaults/main.yml`:
  - Remove `commander_fid_allowlist` var
  - Keep `commander_login_attempt_log`
  - Top-of-file comment rewritten: "access_list role owns the login-attempt
    log directory. Authorization is managed via the Kaine AdminPage and
    Authentik groups."
- `edin-backend/ansible/roles/access_list/README.md`:
  - Rewrite the "Adding a commander" and "Removing a commander" sections:
    Authentik is the source of truth; env-var config is retired.
- `edin-backend/ansible/roles/control_api/templates/control-api.env.j2`:
  - Remove the `COMMANDER_FID_ALLOWLIST={{ commander_fid_allowlist | join(',') }}` line

**Gate:**
```bash
cd edin-backend && go test ./... -count=1
cd ../edin-frontend && npm run test:run
cd ../edin-backend/ansible && source .venv/bin/activate && ansible-playbook -i inventories/prod/hosts.ini site.yml --syntax-check
```

**Acceptance:** Deploy. Ops runbook is the only documented way to add a
commander. Attempting to grep the codebase for `COMMANDER_FID_ALLOWLIST`
returns no hits outside the git history. Env dump inside the container shows
no such variable. Existing users continue to log in via the Authentik path.

---

## Risk register

| Risk | Severity | Mitigation |
|---|---|---|
| Task 2 subtly changes tool visibility (e.g. a tool Kaine could invoke now it can't) | High | Parity tests in Task 2 compare new set to a frozen snapshot of the old sets. Ship Task 1+2 together, smoke-test via representative Kaine/Copilot prompts before moving on. |
| Task 4 migration fails on prod DB | Medium | Migration is additive — reversible with `ALTER TABLE DROP COLUMN`. Embedded migrator runs on backend startup; failure blocks startup (visible, not silent). Test against testcontainers first; dry-run on any available non-prod DB. |
| Authentik API call in the callback path adds latency or fails | Medium | 2s timeout. Deny-closed on error (`reason=authentik_unreachable`). Latency histogram metric lets us SLO this. |
| Admin accidentally approves the wrong FID | Medium | AdminPage shows FID + commander name + last-seen. Every mutating endpoint logs admin identity + action. Unlink/deny is one click. |
| Scope claim inflates JWT size beyond sensible | Low | Even `kaine-god` with every scope is ~120 bytes. Cookie + header limits are far higher. |
| A commander is approved in Authentik but the JWT (24h life) still grants old scopes | Low | Scope ADDITIONS need no revocation (next login picks them up; commander may wait or logout). Scope REMOVALS are forced-revoked via `revokeAllSessions` on Deny / Unlink / group-remove. Documented in runbook. |
| Authentik user deleted while a commander is linked to them | Medium | Dedicated `authentik_user_missing` reason code + deny-closed. Admin UI surfaces broken link with a "Re-link or unlink" CTA. Distinct from `authentik_unreachable` so alerting doesn't conflate the two states. |
| Admin accidentally denies or unlinks themselves (single-admin deployment locks out ops) | Medium | Self-targeted Deny / Unlink / Revoke requires a confirmation dialog in the UI (frontend Task 10). The first-cut env-var allowlist stays live through Task 11 as a break-glass recovery path; after Task 12 the recovery path is DB surgery from the server (documented in runbook). |
| Log injection via malicious Frontier display name forges admin-action or denial lines | Low | `sanitizeLogField` helper applied to every free-text field in text-log paths. JSON audit files use `json.Marshal` which handles escaping. Text logs are for human monitoring; JSON audit is authoritative. |
| CSRF via cross-site POST to admin endpoints while the operator has an active kaine cookie | Medium | `SameSite=Lax` on the kaine cookie blocks the common case. Mutating admin endpoints also require `X-Edin-Fetch: 1` header, which a cross-site form post cannot attach — only same-origin JS can. Dual defence. |
| Authentik compromise silently grants any FID admin scopes | High | Out-of-scope for this plan: Authentik is the accepted trust anchor (see Trust model). Mitigation is the existing Authentik hardening (network isolation, admin group gating). Revisit if Authentik admin access model changes. |
| Auto-link shadow creation fails mid-flow — Authentik created the user but the DB link write failed | Low-Medium | The next callback finds an unlinked row, re-invokes `CreateShadowUser`, which hits duplicate-username on Authentik, falls back to `GetUserByUsername`, retrieves the same UUID, and writes the link. Self-healing. Audited as `link_persist_failed` on the first-attempt denial. |
| Shadow user directory grows unbounded over time | Low | Open question #5 tracks the pruning job. No security impact (shadows have no credentials and no groups by default). Defer until directory size is visibly a problem. |
| Grant endpoint accidentally admits a commander to `kaine-admin` via crafted request body | Medium | Grant's allow-list is narrower than the generic user-group endpoint: only `edin-copilot` / `edin-copilot-trusted` are acceptable. Anything else → 400. Separate handler specifically to prevent this class of slip. |
| Grant partial failure — group added in Authentik but approved-flip failed in DB (or vice versa) | Low | Order is fixed: `AddUserToGroup` first, then `SetApproved(true)`. If AddUserToGroup succeeds and SetApproved fails, commander is in the group but not approved → cannot log in (approved-gate still denies). Operator retries Grant; Authentik's add-user is idempotent. No privilege escalation from partial state. |
| Auto-link triggers Authentik user creation for bot / scraper traffic hitting the callback path | Low | Callback requires a valid Frontier PKCE code, which attackers can't forge without Frontier credentials. Rate limit on `/api/commander/auth/callback` already exempts nothing — a bot that somehow did reach callback would get a valid shadow but no groups/approval, so no effective access. Pollution not compromise. |

## Cutover / rollback

- Tasks 1–11 are fully deployable in any order that preserves dependencies
  and are individually reversible by `git revert` + redeploy backend. Prod
  behaviour is stable throughout because the env-var fallback remains.
- Task 12 is the single one-way moment. Rollback: `git revert` the task
  commit, redeploy, and the env-var allowlist is back — existing linked
  commanders still auth via the Authentik path (it has priority), so nothing
  breaks.

## Open questions

1. ~~**`edin-` or `copilot-` prefix?**~~ **Resolved:** `edin-copilot` and
   `edin-copilot-trusted`. Matches the `edin-*` family naming and separates
   cleanly from `kaine-*`.
2. ~~**Awaiting-approval UX.**~~ **Resolved:** we keep a generic 403 page
   at the callback. Rationale: `awaiting_approval` vs `not_on_allowlist`
   vs `authentik_user_missing` are all operator-visible in the audit log;
   exposing the distinction in the public-facing 403 would leak whether
   a given FID is known to the system. A future task can add an internal
   "contact your admin" copy block, but not a distinct page per reason.
3. ~~**Self-service link.**~~ **Resolved (via auto-link):** first Frontier
   login auto-creates a shadow Authentik user in the
   `users/edin-commanders/` path and links the FID to it — see the
   "Auto-link on first Frontier login" design subsection and Task 5.
   Shadow users are inert (no credentials, no groups) until an admin
   Grants them access via the Commanders tab. Admins can optionally
   re-link a shadow to an existing real Authentik user via Unlink→Link
   (rare, typically only for the admin themselves).
4. ~~**RLS on `commander.commanders`.**~~ **Resolved:** yes — Task 4 adds
   row-level security to `commander.commanders` with a dedicated admin
   role (`edin_cmd_admin`, `BYPASSRLS`) that is `SET LOCAL ROLE`'d only
   inside `withAdminTx`-wrapped transactions. Writers never see other
   commanders' rows.
5. **Shadow user pruning.** Every Frontier login that ever reaches us
   leaves a shadow Authentik user behind forever, even if the admin
   never Grants access. Not a security issue (no creds, no groups), just
   directory cruft. **Open:** add a scheduled prune ("delete shadow
   Authentik users whose commander row has `approved=false` and
   `last_seen_at < now() - interval '180 days'`") as a later task, or
   live with the cruft. Recommend deferring until cruft is visible.
