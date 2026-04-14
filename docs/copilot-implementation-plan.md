# EDIN Copilot — Full Implementation Plan

**Goal:** Build a secure, production-quality AI copilot at `edin.space/copilot/` that authenticates
Elite Dangerous commanders via Frontier OAuth2 PKCE, syncs real-time journal event data from the
edin-client desktop app, and provides an AI chat interface with access to both live game state and
the full edin.space galaxy knowledge base (Memgraph). In doing so, migrate all commander data sync
from the Python EDIN backend into the Go edin-space backend, and retire the Python service.

**Approach:** Test-Driven Development throughout. Every story specifies tests first. Implementation
is complete when all tests pass. No story is accepted without green tests.

**Repositories involved:**
- `~/src/edin-space/edin-backend/` — Go backend (primary target)
- `~/src/edin-space/edin-frontend/` — React/Vite frontend
- `~/src/edin-space/atlas/` — Infrastructure (Caddy, Terraform/Ansible)
- `~/src/edin/edin-client/` — Flutter desktop app (Windows)
- `~/src/edin/edin-backend/` — Python backend (to be decommissioned)

---

## Review Notes (Post Red-Team)

This plan was red-teamed after initial drafting. 24 issues were identified and incorporated below.
Critical findings are noted inline with `⚠️ CRITICAL FIX:` markers. Full finding list at end of doc.

---

## Security Architecture (Decisions Made)

These decisions came out of our red-team review. They are non-negotiable constraints for all
implementation work.

| Decision | Rationale |
|---|---|
| RS256 JWT (asymmetric) | Go backend only needs the public key — cannot forge tokens even if compromised |
| `SET LOCAL` inside explicit transactions for every commander query | Prevents RLS bypass via pgxpool connection reuse |
| Non-superuser DB role for the application | Ensures `FORCE ROW LEVEL SECURITY` is actually enforced |
| Separate DB roles: `edin_cmd_writer` / `edin_cmd_reader` | Compromise of AI query path cannot write data |
| WebSocket auth via first-message frame, not query string | Tokens in query strings appear in access logs |
| httpOnly cookies for copilot frontend JWT | XSS cannot read httpOnly cookies; localStorage is vulnerable |
| `GET /api/*/auth/token` endpoint is single-use + requires CSRF header | Mitigates XSS re-extracting token via fetch; single-use prevents replay |
| `SameSite=Lax` (not Strict) on session cookies | Strict breaks top-level navigation (e.g. Discord links to copilot); Lax allows GET nav, blocks cross-site POST |
| FID sourced from JWT context only, never from request body or tool input | Defence against LLM injection and API abuse |
| RLS policy on `journal_events` as DB-level backstop | Application bugs cannot leak cross-commander data |
| Per-FID rate limiting on ingest and chat (key = FID from JWT, not auth header) | JWT rotation must not reset rate limit counter |
| Per-IP rate limiting on auth initiation | DoS prevention for PKCE state accumulation |
| `jti` claim + Redis revoked set for logout | Enables session invalidation within JWT lifetime |
| PKCE state stored server-side, 10-minute TTL, max 1000 pending | Prevents state accumulation DoS |
| Poll session is single-use: token consumed on first successful poll | Prevents session fixation on desktop client auth flow |
| Frontier scope `auth capi` | Required for `/me` and CAPI endpoints; standard PKCE defaults fail |
| CAPI call has explicit timeout (10s) with graceful fallback | CAPI is unreliable; auth must not fail if CAPI is slow |
| Game data labelled as untrusted user content in LLM context | Mitigates prompt injection via crafted ED strings |
| RSA key minimum 2048 bits enforced at load time | RSA-1024 is breakable; validate at startup |
| DB role creation via Ansible, not SQL migrations | Migrations cannot safely interpolate credentials; Ansible uses vault |
| Migration script uses dedicated `edin_migrator` role with BYPASSRLS | Normal app roles cannot do bulk cross-FID inserts |
| No space partitioning (by_hash) on hypertable | TimescaleDB does not support compression on multi-dimensional hypertables |

---

## Wire Format Reference (edin-client → backend)

Discovered from `lib/services/edin_api_service.dart`. The client currently posts:

**Single event:** `POST /api/v1/ingest/event`
```json
{
  "event": {
    "timestamp": "2026-04-13T23:04:07Z",
    "event": "MissionAccepted",
    "fid": "F2504",
    "commander_name": "Pattern State",
    "event_data": { ...full event JSON... },
    "session_id": "optional-session-uuid"
  }
}
```

**Batch events:** `POST /api/v1/ingest/events`
```json
{
  "events": [
    { "timestamp": "...", "event": "...", "fid": "F2504", "commander_name": "...", "event_data": {...} }
  ]
}
```

Auth header: `Authorization: Bearer <edin_jwt>`

**In our new design:** `fid` in the request body is **logged and compared but not used for storage**.
FID for all storage operations comes exclusively from the validated JWT. If the body `fid` differs
from the JWT `fid`, the discrepancy is logged as a warning and the request continues using the JWT
FID. The body `fid` is never used as a query or storage key.

⚠️ **This is different from rejecting mismatches.** The client sends `fid` as metadata; it is
a backward-compat field being phased out. Silent ignore + log is the right policy. Rejection
would break clients with stale caches. Only log; do not 400.

---

## Epic Overview

| Epic | Title | Sprints |
|---|---|---|
| 0 | Discovery & Test Infrastructure | 0 |
| 1 | Security Hardening — Existing Kaine Chat | 1 |
| 2 | Commander Identity & Frontier Auth (Go) | 1–2 |
| 3 | Commander Data Schema & Repository (Go) | 2 |
| 4 | Commander Event Ingestion (Go) | 2–3 |
| 5 | Commander Query API & AI Tools (Go) | 3 |
| 6 | Copilot Chat WebSocket (Go) | 3–4 |
| 7 | Frontend — Copilot Page | 4–5 |
| 8 | edin-client — Migration to Go Backend | 5 |
| 9 | Infrastructure, Data Migration & Decommission | 6 |

---

## Epic 0: Discovery & Test Infrastructure

**Why:** Before writing any production code, establish the test harness that all subsequent stories
depend on. Integration tests for the commander data layer need a real TimescaleDB instance; we use
`testcontainers-go` to spin one up per test run.

---

### Story 0.1 — Go Integration Test Database Harness

**Complexity:** S

**What:** Add `testcontainers-go` to the Go module and create a shared test helper that:
- Starts a TimescaleDB container (pinned image — see below) for integration tests
- Runs schema migrations to create the commander schema
- Provides a clean `pgxpool.Pool` for each test
- Tears down cleanly after each test suite

⚠️ **CRITICAL:** Use the TimescaleDB image that matches production. Check
`edin-data/ansible/` or `edin-data/docker-compose.yml` for the current production version.
Do NOT use `timescale/timescaledb:latest-pg16` — pin to the exact version tag.
The helper must call `CREATE EXTENSION IF NOT EXISTS timescaledb;` before running migrations —
a plain PostgreSQL container will not have it. `create_hypertable` calls fail silently or panic
without the extension.

**Files to Create:**
- `edin-backend/internal/testutil/db.go` — `StartTestDB(t *testing.T) (*pgxpool.Pool, func())`
- `edin-backend/internal/testutil/migrations.go` — applies SQL schema files in order
- `edin-backend/internal/testutil/fixtures.go` — factory functions for test commanders / events

**Files to Modify:**
- `edin-backend/go.mod` — add `github.com/testcontainers/testcontainers-go`

**Tests (write first):**
```go
// internal/testutil/db_test.go
func TestStartTestDB_StartsAndMigrates(t *testing.T)           // pool pings, schema exists
func TestStartTestDB_TimescaleDBExtensionEnabled(t *testing.T) // SELECT extname FROM pg_extension WHERE extname='timescaledb'
func TestStartTestDB_CleanupDropsData(t *testing.T)            // cleanup leaves no residue
func TestFixtures_CreateCommander(t *testing.T)                // inserts a commander row
func TestFixtures_CreateJournalEvents(t *testing.T)            // inserts events for FID
```

**Acceptance Criteria:**
- `go test ./internal/testutil/...` passes
- Container image tag is hardcoded to match production (reviewed in PR)
- `timescaledb` extension is present in the test DB (verified by test)
- Container starts in < 30s

---

### Story 0.2 — RS256 Key Generation Helper

**Complexity:** S

**What:** A CLI tool (`cmd/keygen/main.go`) that generates an RS256 keypair. Also creates
`internal/auth/keys.go` with `LoadPrivateKey(path)` and `LoadPublicKey(path)` helpers.

⚠️ **Key size validation required:** `LoadPrivateKey` must return an error if the key is
smaller than 2048 bits. `LoadPublicKey` must verify the corresponding public key is RSA.
This prevents accidental use of RSA-1024 keys if someone replaces the keypair.

**Files to Create:**
- `edin-backend/cmd/keygen/main.go` — generates RSA-2048 keypair, prints PEM
- `edin-backend/internal/auth/keys.go` — `LoadPrivateKey`, `LoadPublicKey`

**Tests (write first):**
```go
// internal/auth/keys_test.go
func TestLoadPrivateKey_ValidPEM(t *testing.T)
func TestLoadPrivateKey_InvalidPEM_ReturnsError(t *testing.T)
func TestLoadPrivateKey_1024BitKey_ReturnsError(t *testing.T)    // ⚠️ minimum key size
func TestLoadPublicKey_ValidPEM(t *testing.T)
func TestLoadPublicKey_MismatchedKeyType_ReturnsError(t *testing.T)
func TestRoundtrip_SignAndVerify(t *testing.T)  // sign with private, verify with public
```

**Acceptance Criteria:**
- `go run ./cmd/keygen` outputs valid PEM-encoded RSA-2048 keypair
- Round-trip sign/verify test passes
- RSA-1024 key passed to `LoadPrivateKey` returns typed error, not panic
- Invalid PEM returns a typed error, not a panic

---

## Epic 1: Security Hardening — Existing Kaine Chat

**Why:** Two live security issues in the Kaine portal must be fixed before adding new features.
These fixes establish the secure patterns the copilot will follow from the start.

⚠️ **Stories 1.1 and 1.2 must land in the same PR.** They have a hard dependency:
Story 1.1's `onopen` handler calls `getToken()` which Story 1.2 changes from synchronous
`localStorage.getItem` to an async `fetch('/api/kaine/token')`. Implementing 1.1 first with a
synchronous call means the auth-frame send logic must be rewritten when 1.2 lands.
Write both together so `onopen` is async from day one.

---

### Story 1.1 — WebSocket Auth: Query String → First-Message Frame

**Why:** JWT in `?token=...` appears in Caddy access logs, browser history, and referrer headers.

**Current:** `GET /api/kaine/chat/ws?token=<jwt>` — token in URL
**New:** Unauthenticated upgrade, then server waits up to 5s for auth frame:
`{"type":"auth","token":"<jwt>"}`. Close with 4401 on timeout, 4403 on invalid token.

⚠️ **Reconnect race condition:** The `connect()` function in `useChatSocket.js` is called on
reconnect. In the new flow, `onopen` must fetch the token from `GET /api/kaine/token` (async)
before sending the auth frame. If this fetch takes longer than the server's 5s wait, or if it
returns 401, the connection will close. Handle explicitly:
- Fetch token with a 3s timeout (shorter than server's 5s auth window)
- If token fetch returns 401 or times out: close the WebSocket immediately without waiting for
  the server to close it; set `connectionStatus = 'error'`; do NOT retry (avoid infinite
  reconnect loop generating repeated token endpoint and WebSocket requests)

**Files to Modify:**
- `edin-backend/internal/httpapi/kaine_chat.go` — remove query-string token extraction; add 5s auth-message wait loop at connection start
- `edin-backend/internal/httpapi/kaine.go` — remove `withKaineAuth` from the WebSocket route
- `edin-space/edin-frontend/src/pages/kaine/hooks/useChatSocket.js`
  - `onopen`: async-fetch token from `/api/kaine/token` (3s timeout), then send auth frame
  - Handle close codes 4401 / 4403 → redirect to login
  - Handle token fetch failure → set error state, do NOT reconnect

**Tests (write first):**
```go
// internal/httpapi/kaine_chat_test.go
func TestKaineChatWS_AuthViaFirstMessage_Success(t *testing.T)
func TestKaineChatWS_AuthViaFirstMessage_Timeout_ConnectionClosed4401(t *testing.T)
func TestKaineChatWS_AuthViaFirstMessage_InvalidToken_ConnectionClosed4403(t *testing.T)
func TestKaineChatWS_TokenInQueryString_HasNoEffect(t *testing.T)
func TestKaineChatWS_NormalMessageBeforeAuth_ConnectionClosed(t *testing.T)
```

```javascript
// src/pages/kaine/hooks/useChatSocket.test.js
test('sends auth frame as first message after open')
test('auth frame fetches token from /api/kaine/token not localStorage')
test('token fetch times out after 3s → closes WebSocket, sets error state')
test('token fetch returns 401 → closes WebSocket, does not reconnect')
test('close code 4401 → redirects to login')
test('close code 4403 → redirects to login with error')
```

**Acceptance Criteria:**
- No JWT appears in any URL during WebSocket establishment
- Reconnect on token-fetch failure does NOT loop (max 0 retries on 401)
- Existing Kaine chat session functionality is unbroken

---

### Story 1.2 — Kaine Frontend: localStorage → httpOnly Cookie

**Why:** JWTs in `localStorage` are readable by any JS on the page, including injected XSS scripts.

**What:**
- Callback sets `Set-Cookie: kaine_session=<jwt>; HttpOnly; Secure; SameSite=Lax; Path=/api/kaine`
  ⚠️ **Use `SameSite=Lax`, not `SameSite=Strict`.** Strict blocks cookies on top-level navigations
  from external links (e.g. a Discord link to `edin.space/kaine/chat`). Lax allows GET navigations
  while blocking cross-site POST — which is the right threat model here.
- Add `GET /api/kaine/token`:
  - Validates the `kaine_session` httpOnly cookie
  - Returns a **single-use nonce** (not the full JWT): `{"nonce": "...", "expires_in": 10}`
  - The nonce maps to the JWT server-side in Redis with a 10s TTL
  - The WebSocket auth frame sends the nonce; the backend resolves it to the JWT and consumes it
  - ⚠️ **This endpoint requires a CSRF defence.** Use a custom request header
    `X-Edin-Fetch: 1` that the frontend always sends. Browsers cannot send custom headers on
    cross-origin requests without CORS preflight; same-origin XSS can, but the attacker would
    need the nonce to be usable — and it's single-use, expiring in 10s. Require the custom header
    server-side and reject requests without it.

**Files to Modify:**
- `edin-backend/internal/httpapi/kaine.go`
  - `handleKaineCallback`: set httpOnly SameSite=Lax cookie; do not return token in JSON body
  - Add `handleKaineToken GET /api/kaine/token`: validates cookie, issues single-use nonce, requires `X-Edin-Fetch: 1`
- `edin-space/edin-frontend/src/pages/kaine/context/AuthContext.jsx`
  - Remove all `localStorage.setItem/getItem('kaine_id_token', ...)`
  - `getToken()` calls `GET /api/kaine/token` with `{headers: {'X-Edin-Fetch': '1'}}`, returns nonce
- `edin-space/edin-frontend/src/pages/kaine/services/tokenRefresh.js`
  - Cookie refresh handled server-side; simplify or remove client-side refresh logic

**Tests (write first):**
```go
// internal/httpapi/kaine_test.go
func TestKaineCallback_SetsHttpOnlyCookie(t *testing.T)
func TestKaineCallback_CookieSameSiteLax(t *testing.T)             // NOT Strict
func TestKaineCallback_DoesNotReturnTokenInBody(t *testing.T)
func TestKaineToken_ValidCookie_WithCsrfHeader_ReturnsSingleUseNonce(t *testing.T)
func TestKaineToken_ValidCookie_MissingCsrfHeader_Returns403(t *testing.T)  // CSRF defence
func TestKaineToken_NoCookie_Returns401(t *testing.T)
func TestKaineToken_ExpiredCookie_Returns401(t *testing.T)
func TestKaineToken_NonceSingleUse_SecondCallReturnsNew(t *testing.T)  // nonce consumed, next call issues a new one
```

```javascript
// src/pages/kaine/context/AuthContext.test.jsx
test('getToken() calls /api/kaine/token with X-Edin-Fetch header')
test('getToken() returns nonce not full JWT')
test('no localStorage writes occur during login flow')
test('no localStorage reads occur during login flow')
```

**Acceptance Criteria:**
- `localStorage` contains no JWT after login
- Cookie flags: `HttpOnly`, `Secure`, `SameSite=Lax` confirmed
- `GET /api/kaine/token` without `X-Edin-Fetch: 1` header returns 403
- Kaine chat still works end-to-end

---

## Epic 2: Commander Identity & Frontier Auth (Go Backend)

**Why:** The Go backend has no concept of Frontier authentication. Frontier issues opaque access
tokens (not JWTs), so we verify identity via CAPI, then issue our own RS256 EDIN JWT with the
commander's FID embedded.

---

### Story 2.1 — Config & Key Loading for Commander Auth

**Complexity:** S

**What:** Add `CommanderAuthConfig` to the config struct, wired from environment variables.

```go
type CommanderAuthConfig struct {
    Enabled              bool
    PrivateKeyPath       string
    PublicKeyPath        string
    JWTIssuer            string        // "edin-space"
    JWTExpiry            time.Duration // 24h default
    FrontierClientID     string
    FrontierClientSecret string
    FrontierAuthURL      string        // "https://auth.frontierstore.net"
    FrontierCAPIURL      string        // "https://companion.orerve.net"
    FrontierScope        string        // "auth capi" — ⚠️ required, not optional
    FrontierCAPITimeout  time.Duration // 10s default — ⚠️ CAPI is slow/unreliable
    PKCEStateTTL         time.Duration // 10m
    PKCEMaxPending       int           // 1000
}
```

⚠️ **`FrontierScope` must default to `"auth capi"`** — not `"openid"` or any standard PKCE
default. Using the wrong scope causes Frontier's `/me` and CAPI endpoints to return 403.

⚠️ **`FrontierCAPITimeout` must default to 10s** — CAPI latency frequently exceeds 5s.
The overall auth callback has a 30s handler timeout; CAPI must not consume all of it.

**Files to Modify:**
- `edin-backend/internal/config/config.go`

**Tests (write first):**
```go
// internal/config/config_test.go
func TestConfig_CommanderAuth_LoadsFromEnv(t *testing.T)
func TestConfig_CommanderAuth_DefaultExpiry24h(t *testing.T)
func TestConfig_CommanderAuth_DefaultScopeIsAuthCAPI(t *testing.T)      // "auth capi" not "openid"
func TestConfig_CommanderAuth_DefaultCAPITimeout10s(t *testing.T)
func TestConfig_CommanderAuth_MissingKeys_DisabledByDefault(t *testing.T)
```

**Acceptance Criteria:**
- `FrontierScope` defaults to `"auth capi"` when not explicitly set
- `FrontierCAPITimeout` defaults to 10s
- Missing private/public key paths leave `Enabled = false`

---

### Story 2.2 — RS256 JWT Issuer & Validator for Commander Tokens

**Complexity:** M

**What:** A `CommanderJWTIssuer` that signs EDIN JWTs using RS256. Claims:
- `iss`: "edin-space"
- `sub`: "frontier|{customer_id}"
- `fid`: "F2504"
- `name`: "Pattern State"
- `jti`: `crypto/rand` UUID (for revocation)
- `iat`, `exp`

Also a `CommanderJWTValidator` that verifies RS256 signatures and checks the `jti` revocation set
in Redis.

**Files to Create:**
- `edin-backend/internal/auth/commander_jwt.go`

**Tests (write first):**
```go
// internal/auth/commander_jwt_test.go
func TestCommanderJWT_Issue_ContainsFIDClaim(t *testing.T)
func TestCommanderJWT_Issue_RS256Algorithm(t *testing.T)
func TestCommanderJWT_Issue_JTIIsNonEmpty(t *testing.T)
func TestCommanderJWT_Validate_ValidToken_ReturnsClaims(t *testing.T)
func TestCommanderJWT_Validate_ExpiredToken_ReturnsError(t *testing.T)
func TestCommanderJWT_Validate_WrongIssuer_ReturnsError(t *testing.T)
func TestCommanderJWT_Validate_TamperedSignature_ReturnsError(t *testing.T)
func TestCommanderJWT_Validate_RevokedJTI_ReturnsError(t *testing.T)
func TestCommanderJWT_Validate_DifferentPublicKey_ReturnsError(t *testing.T)
func TestCommanderJWT_Roundtrip_FIDPreserved(t *testing.T)
```

**Acceptance Criteria:**
- Tokens signed with a test private key are rejected by a validator using a different public key
- Revoked `jti` in Redis → validator returns `ErrTokenRevoked`
- `jti` is a UUID-format string

---

### Story 2.3 — Frontier PKCE Auth Endpoints

**Complexity:** L

**What:** Three HTTP endpoints implementing the Frontier PKCE flow for the **web frontend**
(browser-based; callback is a browser redirect). The desktop client uses a separate poll flow
(Story 8.1).

1. **`GET /api/commander/auth/initiate`**
   - Generates PKCE `code_verifier` + `code_challenge` (S256)
   - Generates `state` UUID via `crypto/rand`
   - Stores `{state → code_verifier}` in Redis with 10-minute TTL
   - ⚠️ **`auth_url` must include `scope=auth+capi`** (URL-encoded). Failure to include
     this scope causes Frontier's `/me` endpoint to return 403.
   - Returns `{auth_url: "https://auth.frontierstore.net/auth?client_id=...&scope=auth+capi&..."}` 
   - Rate limited: 5 requests per IP per minute

2. **`GET /api/commander/auth/callback?code=...&state=...`**
   - Looks up `code_verifier` by state; 400 if not found or expired
   - POSTs to `{FrontierAuthURL}/token` with code + verifier
   - Calls `{FrontierAuthURL}/me` to get `customer_id` → derives FID as `F{customer_id}`
   - Calls CAPI `{FrontierCAPIURL}/profile` for commander name
   - ⚠️ **CAPI call uses `FrontierCAPITimeout` (10s). If it fails or times out:**
     - Log the error with request ID
     - Use placeholder name `"Unknown Commander"` and store a flag `capi_pending = true`
     - Do NOT return 502 — the auth should succeed with the FID even if the name is unavailable
     - A subsequent refresh call can attempt CAPI again and update the name
   - Issues RS256 EDIN JWT with FID, name (or placeholder), jti
   - Stores Frontier access token + refresh token in Redis: key `frontier_token:{jti}`,
     TTL = Frontier token `expires_in` (typically 3h). See Story 2.5 for schema.
   - Sets httpOnly cookie `commander_session=<jwt>; Secure; SameSite=Lax; Path=/api/commander`
   - Returns `{commander_name, fid, capi_pending}` JSON
   - Deletes PKCE state from Redis

3. **`POST /api/commander/auth/logout`**
   - Reads `commander_session` cookie
   - Validates JWT, extracts `jti` and `exp`
   - Adds `jti` to Redis revoked set with TTL = remaining JWT lifetime
   - Clears cookie via `Set-Cookie` with `Max-Age=0`

**Files to Create:**
- `edin-backend/internal/httpapi/commander_auth.go`
- `edin-backend/internal/frontier/client.go`
  - `FrontierClient` with `ExchangeCode`, `GetMe`, `GetProfile` methods

**Tests (write first):**
```go
// internal/httpapi/commander_auth_test.go
func TestAuthInitiate_ReturnsAuthURL(t *testing.T)
func TestAuthInitiate_AuthURLContainsCAPIScope(t *testing.T)    // ⚠️ "auth+capi" or "auth%20capi" present
func TestAuthInitiate_StoresStateInRedis(t *testing.T)
func TestAuthInitiate_RateLimit_ExceededReturns429(t *testing.T)

func TestAuthCallback_ValidState_IssuesJWT(t *testing.T)
func TestAuthCallback_InvalidState_Returns400(t *testing.T)
func TestAuthCallback_ExpiredState_Returns400(t *testing.T)
func TestAuthCallback_FrontierExchangeFails_Returns502(t *testing.T)
func TestAuthCallback_CAPIFails_SucceedsWithPlaceholderName(t *testing.T)  // ⚠️ NOT 502
func TestAuthCallback_CAPITimeout_SucceedsWithPlaceholderName(t *testing.T)
func TestAuthCallback_SetsHttpOnlyCookieSameSiteLax(t *testing.T)
func TestAuthCallback_ResponseContainsCAPIPendingFlag(t *testing.T)         // when CAPI failed

func TestAuthLogout_RevokesJTI(t *testing.T)
func TestAuthLogout_ClearsCookie(t *testing.T)
func TestAuthLogout_NoCookie_Returns401(t *testing.T)

// internal/frontier/client_test.go  (uses httptest.Server to mock Frontier)
func TestFrontierClient_ExchangeCode_Success(t *testing.T)
func TestFrontierClient_ExchangeCode_FrontierRejects_ReturnsError(t *testing.T)
func TestFrontierClient_GetMe_ExtractsFID(t *testing.T)
func TestFrontierClient_GetProfile_ExtractsCommanderName(t *testing.T)
func TestFrontierClient_GetProfile_Timeout_ReturnsError(t *testing.T)
func TestFrontierClient_ExchangeCode_ScopeInRequest(t *testing.T)           // verifies scope sent
```

**Acceptance Criteria:**
- `auth_url` contains `scope=auth+capi` (or `scope=auth%20capi`)
- CAPI failure returns 200 with `{capi_pending: true}`, not 502
- httpOnly cookie with `Secure; SameSite=Lax`
- Frontier token stored in Redis under `frontier_token:{jti}`

---

### Story 2.4 — Commander Auth Middleware

**Complexity:** M

**What:** `withCommanderAuth` middleware that:
1. Reads `commander_session` cookie OR `Authorization: Bearer` header (for edin-client)
2. Validates RS256 JWT using `CommanderJWTValidator`
3. Checks `jti` against Redis revoked set
4. Injects `CommanderClaims` (including `FID`) into request context
5. Sets `ScopeCopilotChat` scope in authz context

Cookie name: `commander_session` (consistent with Story 2.3).

`fidFromContext` helper:

⚠️ **Do NOT panic.** The project's CLAUDE.md requires explicit error handling on all paths.
`fidFromContext` returns `(string, error)` with a typed `ErrNoFIDInContext` sentinel.
Callers check the error and return HTTP 500 (internal error — middleware not applied correctly).
This is detectable at code review without needing a runtime panic.

**Files to Create:**
- `edin-backend/internal/httpapi/commander_middleware.go`

**Files to Modify:**
- `edin-backend/internal/authz/authz.go` — add `ScopeCopilotChat Scope = "copilot_chat"`
- `edin-backend/internal/authz/context.go` — ensure scope injection works for new scope

**Tests (write first):**
```go
// internal/httpapi/commander_middleware_test.go
func TestCommanderAuth_ValidCookie_InjectsClaimsToContext(t *testing.T)
func TestCommanderAuth_ValidBearerToken_InjectsClaimsToContext(t *testing.T)
func TestCommanderAuth_NoCookieNoBearer_Returns401(t *testing.T)
func TestCommanderAuth_ExpiredToken_Returns401(t *testing.T)
func TestCommanderAuth_RevokedJTI_Returns401(t *testing.T)
func TestCommanderAuth_FIDFromContext_MatchesJWTClaim(t *testing.T)
func TestCommanderAuth_ScopeCopilotChatInjected(t *testing.T)
func TestFIDFromContext_WithoutMiddleware_ReturnsError(t *testing.T)  // NOT panic
func TestFIDFromContext_Returns500_NotPanic_WhenMisused(t *testing.T)
```

**Acceptance Criteria:**
- `fidFromContext` without middleware returns `("", ErrNoFIDInContext)` — does not panic
- Handler correctly returns HTTP 500 (not crash) when `fidFromContext` returns an error
- Both cookie and Bearer header auth paths work

---

### Story 2.5 — Token Refresh Endpoint & Frontier Token Storage

**Complexity:** M

**What:** `GET /api/commander/auth/refresh` and the Redis schema for storing Frontier tokens.

**Redis key schema:**
```
frontier_token:{jti}  →  JSON {
    "access_token": "...",
    "refresh_token": "...",   // may be absent if Frontier doesn't issue one
    "expires_at": "RFC3339",
    "capi_pending": false
}
TTL = Frontier token expires_in (typically 10800s / 3h)
```

**Refresh flow:**
1. Read `commander_session` cookie; validate EDIN JWT; extract `jti`
2. Look up `frontier_token:{jti}` in Redis
3. If not found (TTL expired): return 401 — user must re-authenticate
4. If Frontier access token is still valid: call `/me` to confirm identity
5. If Frontier access token is expired AND a refresh token is stored: call
   `{FrontierAuthURL}/token` with `grant_type=refresh_token` to get new tokens
6. Store new Frontier tokens under the NEW EDIN JWT's `jti`
7. Issue new EDIN RS256 JWT with fresh `jti` and `exp`; revoke old `jti`
8. Set new `commander_session` cookie

If `capi_pending = true` in the stored token: attempt CAPI `/profile` during refresh.
On success, update the commander name in the new JWT. On failure, keep `capi_pending = true`.

**Files to Modify:**
- `edin-backend/internal/httpapi/commander_auth.go` — add `handleCommanderAuthRefresh`
- `edin-backend/internal/frontier/client.go` — add `RefreshToken` method

**Tests (write first):**
```go
func TestAuthRefresh_ValidToken_IssuesNewJWT(t *testing.T)
func TestAuthRefresh_OldJTIRevoked(t *testing.T)
func TestAuthRefresh_NewJTIIssuedForNewFrontierTokens(t *testing.T)
func TestAuthRefresh_FrontierTokenExpired_UsesRefreshToken(t *testing.T)
func TestAuthRefresh_FrontierTokenExpiredNoRefreshToken_Returns401(t *testing.T)
func TestAuthRefresh_RedisKeyExpired_Returns401(t *testing.T)
func TestAuthRefresh_NoCookie_Returns401(t *testing.T)
func TestAuthRefresh_CAPIPending_RetrySucceeds_UpdatesName(t *testing.T)
```

**Acceptance Criteria:**
- Old `jti` is in revoked set after refresh
- New JWT has new `jti` and fresh expiry
- Frontier refresh token is used if available when access token is expired
- `capi_pending` is cleared in new JWT when CAPI name lookup succeeds on refresh

---

## Epic 3: Commander Data Schema & Repository

**Why:** Secure, performant, multi-tenant time-series schema for commander journal events in the
existing edin.space TimescaleDB instance.

---

### Story 3.1 — Database Schema & Migrations

**Complexity:** M

⚠️ **No space partitioning (`by_hash`).** TimescaleDB does not support compression on
multi-dimensional (space-partitioned) hypertables. The plan originally included `by_hash('fid', 16)`
but this would make the compression policy in Migration 006 fail with an error at migration time.
A single time-dimension hypertable with `(fid, timestamp DESC)` and `(fid, event_type, timestamp DESC)`
indexes provides sufficient query performance. Space partitioning can be revisited if TimescaleDB
adds compression support for it in a future version.

⚠️ **Role creation is NOT in these SQL migrations.** The `CREATE ROLE` statements cannot safely
interpolate passwords in SQL files. Role creation is handled in Story 9.2 via Ansible
(`community.postgresql.postgresql_user`). The migrations assume roles exist; they only contain
`GRANT` statements.

```sql
-- Migration 001: commander schema
CREATE SCHEMA IF NOT EXISTS commander;

-- Migration 002: commanders registry
CREATE TABLE commander.commanders (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    fid           TEXT        UNIQUE NOT NULL,
    cmdr_name     TEXT        NOT NULL,
    capi_pending  BOOLEAN     NOT NULL DEFAULT FALSE,
    platform      TEXT        NOT NULL DEFAULT 'frontier',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE ON commander.commanders TO edin_cmd_writer;
GRANT SELECT                  ON commander.commanders TO edin_cmd_reader;

-- Migration 003: journal events hypertable (single time dimension only)
CREATE TABLE commander.journal_events (
    id             BIGSERIAL,
    commander_id   UUID        NOT NULL REFERENCES commander.commanders(id),
    fid            TEXT        NOT NULL,
    timestamp      TIMESTAMPTZ NOT NULL,
    event_type     TEXT        NOT NULL,
    event_data     JSONB       NOT NULL,
    client_version TEXT,
    ingested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY    (fid, timestamp, id)
);

SELECT create_hypertable('commander.journal_events', by_range('timestamp', INTERVAL '7 days'));

GRANT SELECT, INSERT ON commander.journal_events TO edin_cmd_writer;
GRANT SELECT          ON commander.journal_events TO edin_cmd_reader;

-- Migration 004: RLS (roles must already exist — created by Ansible, Story 9.2)
ALTER TABLE commander.journal_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE commander.journal_events FORCE ROW LEVEL SECURITY;

CREATE POLICY commander_isolation ON commander.journal_events
    AS PERMISSIVE FOR ALL
    TO edin_cmd_reader, edin_cmd_writer
    USING (fid = current_setting('app.current_fid', true));

-- Migration 005: indexes
CREATE INDEX ON commander.journal_events (fid, timestamp DESC);
CREATE INDEX ON commander.journal_events (fid, event_type, timestamp DESC);
CREATE INDEX ON commander.journal_events USING GIN (event_data jsonb_path_ops);

-- Migration 006: compression (valid because hypertable is single-dimension)
ALTER TABLE commander.journal_events SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'fid',
    timescaledb.compress_orderby   = 'timestamp DESC'
);
SELECT add_compression_policy('commander.journal_events', INTERVAL '7 days');
```

**Files to Create:**
- `edin-backend/internal/store/migrations/commander/001_schema.sql`
- `edin-backend/internal/store/migrations/commander/002_commanders_table.sql`
- `edin-backend/internal/store/migrations/commander/003_journal_events_hypertable.sql`
- `edin-backend/internal/store/migrations/commander/004_rls_policies.sql`
- `edin-backend/internal/store/migrations/commander/005_indexes.sql`
- `edin-backend/internal/store/migrations/commander/006_compression.sql`
- `edin-backend/internal/store/commander_migrate.go`

**Tests (write first — integration tests using testutil.StartTestDB):**
```go
// internal/store/commander_migrate_test.go
func TestMigrateCommanderSchema_IdempotentOnRerun(t *testing.T)
func TestMigrateCommanderSchema_CreatesHypertable(t *testing.T)
func TestMigrateCommanderSchema_HypertableIsSingleDimension(t *testing.T)  // ⚠️ no space partitions
func TestMigrateCommanderSchema_CompressionPolicyApplied(t *testing.T)     // ⚠️ must succeed on single-dim
func TestMigrateCommanderSchema_RLSPolicyExists(t *testing.T)
func TestMigrateCommanderSchema_WriterRoleCanInsert(t *testing.T)
func TestMigrateCommanderSchema_ReaderRoleCannotInsert(t *testing.T)
func TestMigrateCommanderSchema_RLSIsolation_ReaderCannotSeeOtherFID(t *testing.T)
```

The RLS isolation test must:
1. Insert events for F001 and F002 as superuser (bypassing RLS for test setup)
2. Connect as `edin_cmd_reader`
3. `BEGIN; SET LOCAL app.current_fid = 'F001'; SELECT ...` → assert only F001 rows
4. `COMMIT; BEGIN; SET LOCAL app.current_fid = 'F002'; SELECT ...` → assert only F002 rows
5. Outside a transaction: `SET LOCAL app.current_fid = 'F001'; SELECT ...` → assert zero rows
   (SET LOCAL has no effect outside a transaction; current_setting returns empty string; RLS returns nothing)

⚠️ **Step 5 is the critical safety check:** it verifies that `SET LOCAL` without a transaction
does NOT leak data — the safe-by-default behaviour.

**Acceptance Criteria:**
- `TestMigrateCommanderSchema_HypertableIsSingleDimension` confirms 0 space dimensions
- `TestMigrateCommanderSchema_CompressionPolicyApplied` passes (would fail with space partitioning)
- `FORCE ROW LEVEL SECURITY` confirmed on `journal_events` (query `pg_policies`)
- Reader role insert attempt returns permission error
- Step 5 of RLS isolation test returns zero rows

---

### Story 3.2 — Commander Repository (Go)

**Complexity:** M

**What:** The Go data access layer for commander schema, enforcing `SET LOCAL` within explicit
transactions on every query.

⚠️ **`withFIDContext` must handle deferred rollback errors correctly.** Do not silently drop
rollback errors. The project's CLAUDE.md mandates explicit error handling:

```go
func (r *pgCommanderRepository) withFIDContext(ctx context.Context, fid string,
    fn func(tx pgx.Tx) error) (retErr error) {

    tx, err := r.pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("begin transaction: %w", err)
    }
    defer func() {
        if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
            // tx.Rollback after a successful Commit returns ErrTxClosed — ignore that.
            // Any other rollback error is real and should be logged.
            r.logger.Error("transaction rollback failed", "error", rbErr, "fid", fid)
            if retErr == nil {
                retErr = fmt.Errorf("rollback: %w", rbErr)
            }
        }
    }()

    if _, err := tx.Exec(ctx, "SET LOCAL app.current_fid = $1", fid); err != nil {
        return fmt.Errorf("set fid context: %w", err)
    }

    if err := fn(tx); err != nil {
        return err
    }
    return tx.Commit(ctx)
}
```

**Pool reuse concurrency test:** Pin the pool to `MaxConns: 1` in the test fixture to guarantee
connection reuse between sequential goroutines. With an unbounded pool in CI, the concurrency test
may use separate connections and never actually test the SET LOCAL leakage scenario.

```go
// CommanderRepository interface
type CommanderRepository interface {
    UpsertCommander(ctx context.Context, fid, name, platform string) (uuid.UUID, error)
    InsertEvents(ctx context.Context, fid string, events []JournalEvent) (inserted, duplicates int, err error)
    RecentEvents(ctx context.Context, fid string, count int) ([]JournalEvent, error)
    EventsByType(ctx context.Context, fid string, types []string, since, until time.Time) ([]JournalEvent, error)
    CurrentLocation(ctx context.Context, fid string) (*LocationState, error)
    DeleteAllEvents(ctx context.Context, fid string) error  // GDPR erasure — see below
}
```

⚠️ **`DeleteAllEvents` must handle compressed TimescaleDB chunks.** `DELETE` on a compressed
chunk raises `ERROR: cannot update/delete rows from chunk that is compressed`. Events older than
7 days are compressed by the policy in Migration 006. The GDPR erasure path will always hit
compressed chunks for any commander who hasn't played in a week.

`DeleteAllEvents` implementation must:
1. Find all compressed chunks containing the target FID's data:
   ```sql
   SELECT chunk_schema, chunk_name FROM timescaledb_information.chunks
   WHERE hypertable_name = 'journal_events' AND is_compressed = true;
   ```
2. Decompress each chunk: `SELECT decompress_chunk(format('%I.%I', chunk_schema, chunk_name)::regclass)`
3. `DELETE FROM commander.journal_events WHERE fid = $1`
4. Optionally re-compress: `SELECT compress_chunk(...)` on affected chunks

This operation is slow (seconds to minutes for large datasets) and must run asynchronously or with
an appropriate HTTP timeout on the erasure endpoint.

**Files to Create:**
- `edin-backend/internal/store/commander_repository.go`

**Tests (write first — integration tests):**
```go
// internal/store/commander_repository_test.go
func TestCommanderRepo_UpsertCommander_CreatesNew(t *testing.T)
func TestCommanderRepo_UpsertCommander_UpdatesExisting(t *testing.T)
func TestCommanderRepo_InsertEvents_StoresEvents(t *testing.T)
func TestCommanderRepo_InsertEvents_DeduplicatesOnTimestampAndType(t *testing.T)
func TestCommanderRepo_InsertEvents_UsesContextFID_IgnoresBodyFID(t *testing.T)
func TestCommanderRepo_RecentEvents_ReturnsNewestFirst(t *testing.T)
func TestCommanderRepo_RecentEvents_RLSIsolation(t *testing.T)

// ⚠️ Pool reuse safety — pin pool to MaxConns=1
func TestCommanderRepo_PoolReuse_NoCrossContamination(t *testing.T)
// Runs 100 sequential queries alternating between F001 and F002 on the same pool.
// Each must return only the correct FID's data.

// ⚠️ SET SESSION vs SET LOCAL correctness
func TestCommanderRepo_SetLocal_ExpiresAfterCommit(t *testing.T)
// After withFIDContext commits, run a bare query on the same connection.
// Verify current_setting('app.current_fid', true) is '' (empty).
// This would FAIL if the implementation used SET SESSION instead of SET LOCAL.

// ⚠️ GDPR erasure with compressed chunks
func TestCommanderRepo_DeleteAllEvents_CompressedChunks_Succeeds(t *testing.T)
// Insert events, wait for/trigger compression, then delete — verify succeeds.

func TestCommanderRepo_DeleteAllEvents_OnlyDeletesTargetFID(t *testing.T)
func TestCommanderRepo_CurrentLocation_ExtractsFromEvents(t *testing.T)
func TestCommanderRepo_WithFIDContext_RollbackErrorsLogged(t *testing.T)
```

**Acceptance Criteria:**
- `TestCommanderRepo_PoolReuse_NoCrossContamination` passes with `MaxConns=1`
- `TestCommanderRepo_SetLocal_ExpiresAfterCommit` would FAIL with `SET SESSION` implementation
- `TestCommanderRepo_DeleteAllEvents_CompressedChunks_Succeeds` confirms GDPR path works
- Deferred rollback error is logged (not silently dropped)

---

## Epic 4: Commander Event Ingestion

**Why:** The edin-client needs an endpoint in the Go backend to sync journal events. Secure
write path: FID from JWT only, validated input, rate limited per FID.

---

### Story 4.1 — Event Ingest Endpoints

**Complexity:** M

**What:** Two HTTP endpoints matching the existing edin-client wire format:
- `POST /api/v1/ingest/event` — single event
- `POST /api/v1/ingest/events` — batch (up to 500 events)

Both require `Authorization: Bearer <edin_jwt>`. FID comes from the JWT.

**Body `fid` policy:**
- If body `fid` matches JWT `fid`: silent, no log needed
- If body `fid` differs from JWT `fid`: log a warning with both values; continue using JWT FID
- If body `fid` is absent: fine, use JWT FID
- **Do NOT return 400 for mismatches.** The field is legacy; rejection breaks backward compat.

**Rate limiting:**
⚠️ **Do NOT use the existing `rateLimitKey(r)` from `middleware.go`** — it uses the full
`Authorization` header string as the key. JWT rotation (refresh) changes the header value and
resets the rate limit counter for the same commander. Extract the FID from the JWT claims in
context and use it as the rate limit key for ingest endpoints.

```go
// Correct rate limit key for ingest:
fid, _ := fidFromContext(r.Context())
if !s.ingestRateLimiter.Allow(fid) { ... }
```

This requires a **separate rate limiter** for ingest (per-FID) distinct from the global
rate limiter (per-IP or auth header). Add an `ingestRateLimiter` field to `Server`.

Validation rules:
- Max body size: 2MB (`http.MaxBytesReader`)
- Max batch size: 500 events
- Timestamp must be within `[now - 365 days, now + 5 minutes]`
- Event type must be in the allowed ED event type allowlist
- Unknown event types: reject entire batch with 400 (fail-fast; client should send known types only)

**Files to Create:**
- `edin-backend/internal/httpapi/commander_ingest.go`
- `edin-backend/internal/httpapi/ed_event_types.go`

**Files to Modify:**
- `edin-backend/internal/httpapi/server.go` — add `ingestRateLimiter` field; register routes

**Tests (write first):**
```go
// internal/httpapi/commander_ingest_test.go
func TestIngestSingle_ValidEvent_Returns200(t *testing.T)
func TestIngestSingle_MissingAuth_Returns401(t *testing.T)
func TestIngestSingle_FIDFromJWT_BodyFIDIgnored(t *testing.T)
func TestIngestSingle_BodyFIDMismatch_LogsWarningAndContinues(t *testing.T)  // ⚠️ NOT 400
func TestIngestSingle_FutureDatedTimestamp_Returns400(t *testing.T)
func TestIngestSingle_TooOldTimestamp_Returns400(t *testing.T)
func TestIngestSingle_UnknownEventType_Returns400(t *testing.T)
func TestIngestSingle_OversizedPayload_Returns413(t *testing.T)

func TestIngestBatch_ValidBatch_Returns200WithCounts(t *testing.T)
func TestIngestBatch_ExceedsMaxSize_Returns400(t *testing.T)
func TestIngestBatch_UnknownEventType_RejectsEntireBatch(t *testing.T)
func TestIngestBatch_Deduplication_DoesNotDoubleInsert(t *testing.T)

// ⚠️ Rate limit must use FID, not auth header
func TestIngestBatch_RateLimit_ByFIDNotAuthHeader(t *testing.T)
func TestIngestBatch_RateLimit_PersistsAcrossTokenRefresh(t *testing.T)
// Issue two different tokens for same FID (old + new after refresh).
// Exhaust rate limit with first token. Second token (same FID) should still be rate-limited.
```

**Acceptance Criteria:**
- Body FID mismatch produces a log warning but returns 200 (not 400)
- Rate limit key is FID from JWT, not the Authorization header string
- Rate limit persists after token refresh (same FID, different JWT = same rate limit counter)
- Response: `{"events_written": N, "events_duplicated": M}`

---

### Story 4.2 — Event Type Allowlist

**Complexity:** S

**Files to Create:**
- `edin-backend/internal/httpapi/ed_event_types.go` — `var AllowedEDEventTypes = map[string]bool{...}`

**Tests (write first):**
```go
func TestAllowedEventTypes_ContainsCoreEvents(t *testing.T)  // FSDJump, Docked, MissionAccepted, etc.
func TestAllowedEventTypes_RejectsUnknown(t *testing.T)
func TestAllowedEventTypes_IsCaseSensitive(t *testing.T)     // "fsdjump" != "FSDJump"
func TestAllowedEventTypes_MinimumCount150(t *testing.T)     // at least 150 known types
```

**Acceptance Criteria:**
- At minimum: FSDJump, Docked, Undocked, MissionAccepted, MissionCompleted, MissionFailed,
  MissionAbandoned, Location, SupercruiseEntry, SupercruiseExit, Scan, SellExplorationData,
  MultiSellExplorationData, MarketSell, MarketBuy, Cargo, LoadGame, Loadout, Materials,
  Rank, Progress, Reputation, Statistics, plus combat, powerplay, carrier events
- Count ≥ 150

---

## Epic 5: Commander Query API & AI Tools

**Why:** The AI copilot needs structured access to commander game state. FID is injected from
auth context — tool input cannot specify or override the FID.

---

### Story 5.1 — Commander Query REST Endpoints

**Complexity:** M

**Files to Create:**
- `edin-backend/internal/httpapi/commander_query.go`

**Endpoints (all protected by `withCommanderAuth`):**
- `GET /api/commander/events/recent?count=20`
- `GET /api/commander/events?types=MissionAccepted,Docked&since=...&until=...`
- `GET /api/commander/location`
- `GET /api/commander/profile`
- `DELETE /api/commander/data` — GDPR erasure

⚠️ **GDPR delete is async.** Decompressing and deleting compressed TimescaleDB chunks takes
seconds to minutes. The endpoint should:
1. Require header `X-Confirm-Delete: I understand this is permanent`
2. Enqueue a background job (or run in a goroutine)
3. Return 202 Accepted with a job ID
4. Provide `GET /api/commander/data/status?job_id=...` for deletion status

**Tests (write first):**
```go
func TestCommanderRecentEvents_Returns200(t *testing.T)
func TestCommanderRecentEvents_NoAuth_Returns401(t *testing.T)
func TestCommanderRecentEvents_FIDQueryParamHasNoEffect(t *testing.T)  // ?fid= ignored
func TestCommanderEvents_FiltersByType(t *testing.T)
func TestCommanderEvents_FiltersByTimeRange(t *testing.T)
func TestCommanderLocation_ReturnsCurrentSystem(t *testing.T)
func TestCommanderLocation_NoEvents_Returns404(t *testing.T)
func TestCommanderDataDelete_ValidHeader_Returns202(t *testing.T)
func TestCommanderDataDelete_MissingHeader_Returns400(t *testing.T)
func TestCommanderDataDelete_OnlyDeletesOwnData(t *testing.T)
func TestCommanderDataDeleteStatus_Returns200WithStatus(t *testing.T)
```

**Acceptance Criteria:**
- `?fid=` parameter has no effect — documented in handler comment with explicit ignore
- GDPR delete returns 202, not 200 (async)
- Confirmation header is checked before any DB operation starts

---

### Story 5.2 — `commander_events` AI Tool

**Complexity:** S

**What:** A tool for `ScopeCopilotChat`. FID is NOT in the tool schema — it comes from the authz
context. Tool signature:

```
Name: "commander_events"
Parameters: { count: int (1-50, default 20), event_types: []string (optional) }
```

⚠️ **`describe_tool` reveals tool names to the LLM.** `describe_tool` is in the copilot allowed
list (Story 6.2). When the LLM calls it, it will learn tool names including `commander_events`.
This is acceptable — the tool name itself is not a security risk. What matters is that FID
cannot be injected via tool input. The system prompt acceptance criterion "does not reveal tool
names" applies only to the system prompt text, not to `describe_tool` responses. This is a known
design tension; it is explicitly accepted.

**Files to Modify:**
- `edin-backend/internal/tools/tools_definitions.go`
- `edin-backend/internal/tools/executor.go`

**Files to Create:**
- `edin-backend/internal/tools/tools_commander.go`

**Tests (write first):**
```go
func TestCommanderEventsTool_UsesFIDFromContext_NotInput(t *testing.T)
func TestCommanderEventsTool_InputFIDIgnored(t *testing.T)       // {"fid":"F9999"} → context FID used
func TestCommanderEventsTool_NoFIDInContext_ReturnsError(t *testing.T)
func TestCommanderEventsTool_FiltersByEventType(t *testing.T)
func TestCommanderEventsTool_RespectsCountLimit50(t *testing.T)
func TestCommanderEventsTool_NotInKaineScope(t *testing.T)       // ScopeKaineChat → tool unavailable
func TestCommanderEventsTool_GameDataLabelledUntrusted(t *testing.T)
```

**Acceptance Criteria:**
- Tool JSON schema contains no `fid` field
- Passing `{"fid": "F9999", "count": 5}` uses context FID, not F9999
- Tool unavailable under `ScopeKaineChat`

---

### Story 5.3 — `commander_location` AI Tool

**Complexity:** S

Returns current system, station, body from most recent location events.

**Files to Modify:**
- `edin-backend/internal/tools/tools_definitions.go`, `executor.go`

**Files to Modify (add to):**
- `edin-backend/internal/tools/tools_commander.go`

**Tests (write first):**
```go
func TestCommanderLocationTool_ReturnsMostRecentSystem(t *testing.T)
func TestCommanderLocationTool_DockingState_IncludesStation(t *testing.T)
func TestCommanderLocationTool_InSupercruise_NoStation(t *testing.T)
func TestCommanderLocationTool_NoLocationEvents_Returns404Message(t *testing.T)
func TestCommanderLocationTool_FIDFromContextOnly(t *testing.T)
```

**Acceptance Criteria:**
- Returns `{system, station, body, docked, supercruise, last_updated}` JSON
- Station is `null` when not docked

---

## Epic 6: Copilot Chat WebSocket

---

### Story 6.1 — Copilot System Prompt

**Complexity:** S

**Files to Create:**
- `edin-backend/internal/httpapi/copilot_prompt.go`

**Tests (write first):**
```go
func TestCopilotPrompt_ContainsCommanderName(t *testing.T)
func TestCopilotPrompt_MentionsGalaxyAndGameState(t *testing.T)
func TestCopilotPrompt_DoesNotRevealToolNamesInSystemPromptText(t *testing.T)
// ⚠️ Note: describe_tool WILL reveal tool names when called by the LLM.
// This test only checks the system prompt string, not describe_tool output.
// The design tension is acknowledged and accepted — see Story 5.2.
func TestCopilotPrompt_ContainsUntrustedDataWarning(t *testing.T)
```

---

### Story 6.2 — Copilot WebSocket Handler, Scope & Routes

**Complexity:** L

⚠️ **`betaToolDefsForContext` in `runner.go` must be updated.** It currently has no case for
`ScopeCopilotChat` and falls through to `return nil` (empty tool list). The copilot runner
would have a working chat but zero tools — a silent functional failure. `runner.go` must be
added to the Files to Modify list.

```go
// internal/assistant/runner.go — add this case:
for _, s := range scopes {
    if s == authz.ScopeCopilotChat {
        return tools.SlimBetaToolDefinitionsForScope(authz.ScopeCopilotChat)
    }
}
```

Also add `ScopeCopilotChat` case to `executor.go`'s allowed-tool map.

**Allowed tools for `ScopeCopilotChat`:**
- `galaxy_system`, `galaxy_station`, `galaxy_power`, `galaxy_faction`, `galaxy_market`
- `galaxy_query`, `galaxy_stats`, `galaxy_bodies`, `galaxy_signals`, `galaxy_fleet_carrier`
- `galaxy_powerplay_cycle`, `galaxy_history`, `galaxy_nearby_powerplay`
- `galaxy_expansion_check`, `galaxy_expansion_frontier`, `galaxy_expansion_targets`
- `spansh_query`, `retrieve_route`, `system_profile`
- `describe_tool`
- `commander_events`, `commander_location`
- **Excluded:** `galaxy_plasmium_buyers`, `galaxy_ltd_buyers`
- **Excluded:** All ops/admin tools

WebSocket auth: first-message frame (same pattern as Story 1.1). The auth frame contains the
single-use nonce obtained from `GET /api/commander/auth/token`, not the full JWT.

**Files to Create:**
- `edin-backend/internal/httpapi/copilot_chat.go`
- `edin-backend/internal/httpapi/copilot_routes.go`

**Files to Modify:**
- `edin-backend/internal/httpapi/server.go` — add `copilotRunner`, wire routes
- `edin-backend/internal/assistant/runner.go` — ⚠️ add `ScopeCopilotChat` case to `betaToolDefsForContext`
- `edin-backend/internal/tools/tools_definitions.go` — add copilot scope filter function
- `edin-backend/internal/tools/executor.go` — add copilot allowed-tool map
- `edin-backend/internal/authz/authz.go` — `ScopeCopilotChat` constant

**Tests (write first):**
```go
// internal/httpapi/copilot_chat_test.go
func TestCopilotWS_AuthViaFirstMessage_Success(t *testing.T)
func TestCopilotWS_NoAuthMessage_ClosedAfter5s(t *testing.T)
func TestCopilotWS_InvalidToken_Closed4403(t *testing.T)
func TestCopilotWS_ValidAuth_ReceivesConnectedMessage(t *testing.T)
func TestCopilotWS_ToolCallUsesContextFID(t *testing.T)
func TestCopilotWS_KaineSpecificToolsUnavailable(t *testing.T)   // plasmium/ltd not in list
func TestCopilotWS_CommanderToolsAvailable(t *testing.T)         // commander_events in list
func TestCopilotWS_SystemPromptContainsCommanderName(t *testing.T)

// ⚠️ Verify betaToolDefsForContext returns tools for copilot scope (not empty list)
func TestBetaToolDefs_CopilotScope_ReturnsNonEmptyToolList(t *testing.T)
func TestBetaToolDefs_CopilotScope_HasCommanderEvents(t *testing.T)
func TestBetaToolDefs_CopilotScope_HasGalaxySystem(t *testing.T)
func TestBetaToolDefs_CopilotScope_DoesNotHavePlasmiumBuyers(t *testing.T)

// internal/assistant/runner_test.go  (add to existing)
func TestRunner_CopilotScope_ToolListNonEmpty(t *testing.T)
```

**Acceptance Criteria:**
- `betaToolDefsForContext` with `ScopeCopilotChat` returns a non-empty tool list
- `plasmium_buyers` and `ltd_buyers` absent from copilot tool list
- `commander_events` present in copilot tool list
- WebSocket route: `GET /api/copilot/chat/ws`

---

## Epic 7: Frontend — Copilot Page

---

### Story 7.1 — Frontier Auth Context

**Complexity:** M

**What:** `FrontierAuthContext.jsx` managing the PKCE auth flow via the backend.

- `initiate()` — `GET /api/commander/auth/initiate`, redirects to returned `auth_url`
- `handleCallback(code, state)` — `GET /api/commander/auth/callback?code=&state=`
  Backend sets httpOnly cookie; response contains `{commander_name, fid, capi_pending}`
- `isAuthenticated()` — `GET /api/commander/auth/status` (reads cookie server-side)
- `logout()` — `POST /api/commander/auth/logout`
- No JWT in frontend memory, sessionStorage, or localStorage

For WebSocket auth, the frontend calls `GET /api/commander/auth/token` with `X-Edin-Fetch: 1`
to get a single-use nonce. This nonce is sent in the WebSocket auth frame. See Story 1.2 for
the CSRF + single-use nonce design.

⚠️ **Add `GET /api/commander/auth/status` endpoint to `commander_auth.go`** — required by the
frontend's `isAuthenticated()`. Returns `{authenticated, commander_name, fid}` or 401.
This endpoint is read-only and validates the cookie without returning any token.

**Files to Create:**
- `edin-frontend/src/pages/copilot/context/FrontierAuthContext.jsx`
- `edin-frontend/src/pages/copilot/context/FrontierAuthContext.test.jsx`

**Files to Modify:**
- `edin-backend/internal/httpapi/commander_auth.go` — add `handleCommanderAuthStatus`

**Tests (write first, Vitest):**
```javascript
test('initiate() calls /api/commander/auth/initiate and redirects')
test('handleCallback() calls backend and reads commander_name from response')
test('isAuthenticated() returns false when backend returns 401')
test('isAuthenticated() returns commander_name when authenticated')
test('logout() calls POST /api/commander/auth/logout')
test('no JWT stored in localStorage at any point')
test('no JWT stored in sessionStorage at any point')
test('getWSNonce() calls /api/commander/auth/token with X-Edin-Fetch header')
```

**Acceptance Criteria:**
- Zero `localStorage.setItem` or `sessionStorage.setItem` calls in the copilot context
- `GET /api/commander/auth/status` endpoint exists and returns 401 for unauthenticated requests

---

### Story 7.2 — Login, Callback, and Chat Pages

**Complexity:** M

**Files to Create:**
- `edin-frontend/src/pages/copilot/LoginPage.jsx`
- `edin-frontend/src/pages/copilot/CallbackPage.jsx`
- `edin-frontend/src/pages/copilot/ChatPage.jsx`
- `edin-frontend/src/pages/copilot/hooks/useCopilotSocket.js`
- `edin-frontend/src/pages/copilot/index.jsx`

**Tests (write first):**
```javascript
// LoginPage.test.jsx
test('renders "Login with Frontier" button')
test('button click calls FrontierAuthContext.initiate()')

// CallbackPage.test.jsx
test('calls handleCallback with code and state from URL')
test('redirects to /copilot/chat on success')
test('redirects to /copilot/login?error=... on failure')

// useCopilotSocket.test.js
test('connects to /api/copilot/chat/ws not /api/kaine/chat/ws')
test('fetches nonce from /api/commander/auth/token with X-Edin-Fetch header')
test('sends nonce as auth frame after open')
test('nonce fetch 401 → closes WS, sets error, does not reconnect')
test('nonce fetch timeout → closes WS, sets error')
test('handles tool_start, tool_complete, text_delta, done events')
test('close code 4403 → redirects to login')
```

**Acceptance Criteria:**
- Chat is visually consistent with Kaine chat
- `/copilot/login`, `/copilot/callback`, `/copilot/chat` route correctly

---

### Story 7.3 — App Routes & Navigation

**Complexity:** S

**Files to Modify:**
- `edin-frontend/src/App.jsx` — add `/copilot/*` routes
- `edin-frontend/vite.config.js` — SPA fallback for `/copilot/*` in dev

**Tests:**
```javascript
test('navigating to /copilot/login renders Copilot login page')
test('navigating to /copilot/chat without auth redirects to /copilot/login')
```

---

## Epic 8: edin-client — Migration to Go Backend

---

### Story 8.1 — Go Backend: edin-client Desktop Auth Flow (Poll-Based)

**Complexity:** M

**What:** Desktop app cannot receive browser redirects directly. Uses poll-based PKCE:

1. `POST /api/v1/auth/frontier/initiate`
   - Generates PKCE pair, `state`, `session_id` (crypto/rand UUID)
   - Stores `{session_id → {state, code_verifier, status: "pending"}}` in Redis, 10min TTL
   - Returns `{auth_url, session_id}`

2. `GET /api/v1/auth/frontier/callback?code=...&state=...`
   - Looks up `session_id` by `state` from Redis
   - Exchanges code, calls CAPI, issues EDIN JWT (same as Story 2.3 callback)
   - Stores `{session_id → {status: "complete", token: "<edin_jwt>"}}` in Redis, 5min TTL

3. `GET /api/v1/auth/frontier/poll?session_id=...`
   - Returns `{status: "pending"}` (202), `{status: "complete", token: "..."}` (200), or 410 if expired
   - ⚠️ **Single-use:** on returning `"complete"` with the token, immediately delete the
     Redis key. A second poll on the same `session_id` returns 410 (Gone), not the token again.
     This prevents session fixation if the `session_id` is guessed or intercepted.

4. `POST /api/v1/auth/refresh` — same as Story 2.5

**Files to Create:**
- `edin-backend/internal/httpapi/commander_client_auth.go`

**Tests (write first):**
```go
func TestClientAuthInitiate_ReturnsAuthURLAndSessionID(t *testing.T)
func TestClientAuthInitiate_SessionIDIsCryptoRandom(t *testing.T)   // not sequential
func TestClientAuthPoll_Pending_Returns202(t *testing.T)
func TestClientAuthPoll_Complete_ReturnsEDINJWT_AndDeletesSession(t *testing.T)
func TestClientAuthPoll_AfterComplete_Returns410(t *testing.T)       // ⚠️ single-use
func TestClientAuthPoll_InvalidSessionID_Returns404(t *testing.T)
func TestClientAuthPoll_ExpiredSession_Returns410(t *testing.T)
func TestClientAuthCallback_StoresTokenForPolling(t *testing.T)
func TestClientAuthRefresh_ValidToken_ReturnsNewToken(t *testing.T)
```

**Acceptance Criteria:**
- Poll session is deleted immediately after first successful retrieval
- Second poll on completed session returns 410 Gone (not 200 with token)
- `session_id` is `crypto/rand` UUID (128 bits entropy)

---

### Story 8.2 — edin-client: Add Go Backend Environment

**Complexity:** S

**Files to Modify:**
- `edin-client/lib/services/auth_service.dart` — add `espace` to `EDINEnvironment`
- `edin-client/lib/services/settings_service.dart` — keep existing default; add espace as option

**Tests (Flutter):**
```dart
test('EDINEnvironment.espace has correct backend URL');
test('initiate auth against Go backend returns auth_url and session_id');
test('poll against Go backend returns EDIN JWT after successful auth');
test('EDIN JWT from Go backend contains fid claim');
```

**Acceptance Criteria:**
- Developer can switch to `espace` environment in settings and authenticate
- Events appear in Go backend TimescaleDB after switching

---

### Story 8.3 — Validate Ingest Endpoint Compatibility

**Complexity:** S

Confirm `edin_api_service.dart` wire format works against Go backend with no client code changes.

**Tests (Flutter):**
```dart
test('uploadSingleEvent succeeds against Go backend');
test('uploadBatchEvents succeeds against Go backend');
test('response format {events_written, events_duplicated} matches expected schema');
test('body fid mismatch logs warning but returns 200');  // ⚠️ NOT 400
```

**Acceptance Criteria:**
- No changes to `edin_api_service.dart` required
- Events stored with correct FID from JWT

---

## Epic 9: Infrastructure, Data Migration & Decommission

---

### Story 9.1 — Caddy Configuration Updates

**Complexity:** S

Add proxy rules:
- `/copilot/*` → edin-frontend static files
- `/api/commander/*` → Go backend
- `/api/v1/ingest/*` → Go backend
- `/api/v1/auth/frontier/*` → Go backend (desktop client auth)
- Remove routes previously pointing to Python backend

⚠️ **WebSocket proxy:** `/api/copilot/chat/ws` requires WebSocket-aware proxying. Caddy's
`reverse_proxy` handles WebSocket upgrades automatically, but the config should explicitly use
the same pattern as the existing `/api/kaine/chat/ws` entry. Reference and copy it.

⚠️ **httpOnly cookies through Caddy:** Caddy must not strip `Set-Cookie` headers. Confirm
no `strip_headers` or response header manipulation is applied to commander auth routes.

**Files to Modify:**
- `atlas/caddy/Caddyfile` (or equivalent)

**Acceptance Criteria:**
- `curl https://edin.space/api/commander/auth/initiate` → 200 with `auth_url`
- `curl https://edin.space/copilot/` → serves React app
- WebSocket connection to `wss://edin.space/api/copilot/chat/ws` upgrades successfully
- `Set-Cookie` headers from commander auth endpoints reach the browser intact

---

### Story 9.2 — Production DB Role Provisioning

**Complexity:** S

⚠️ **Role creation belongs here, not in SQL migrations.** Passwords cannot be safely
interpolated in SQL files. Use Ansible `community.postgresql.postgresql_user` with credentials
from Ansible Vault.

Ansible tasks:
```yaml
- name: Create edin_cmd_writer role
  community.postgresql.postgresql_user:
    name: edin_cmd_writer
    password: "{{ vault_edin_cmd_writer_password }}"
    role_attr_flags: LOGIN,NOSUPERUSER,NOBYPASSRLS

- name: Create edin_cmd_reader role
  community.postgresql.postgresql_user:
    name: edin_cmd_reader
    password: "{{ vault_edin_cmd_reader_password }}"
    role_attr_flags: LOGIN,NOSUPERUSER,NOBYPASSRLS

- name: Create edin_migrator role (migration use only, temporary BYPASSRLS)
  community.postgresql.postgresql_user:
    name: edin_migrator
    password: "{{ vault_edin_migrator_password }}"
    role_attr_flags: LOGIN,NOSUPERUSER,BYPASSRLS
```

Add `EDIN_CMD_WRITER_PASSWORD`, `EDIN_CMD_READER_PASSWORD` to Go backend environment (via Ansible Vault).

**Files to Modify:**
- `atlas/ansible/` — new role provisioning tasks

**Acceptance Criteria:**
- Neither `edin_cmd_writer` nor `edin_cmd_reader` has `SUPERUSER` or `BYPASSRLS` (verified: `\du`)
- `edin_migrator` has `BYPASSRLS` (for data migration Story 9.3)
- Connection test from Go backend to TimescaleDB passes as both writer and reader roles

---

### Story 9.3 — Data Migration from Python Backend

**Complexity:** M

One-time migration from Python backend's TimescaleDB into the Go backend's `commander` schema.

**Migration script (`cmd/migrate-commander-data/main.go`):**
- Connects to source DB as read-only user
- Connects to target DB as **`edin_migrator`** (has BYPASSRLS — required for cross-FID bulk insert)
- For each FID in source: upsert into `commander.commanders`, then bulk-insert `journal_events`
- Unknown event types from Python backend: skip and log (Python may have stored events that Go's
  allowlist rejects)
- `--dry-run` flag: report what would be migrated without writing
- Reports: total FIDs, total events, skipped (unknown type), inserted, duplicates

⚠️ **The `edin_migrator` role must be used — not `edin_cmd_writer`.** Writer role has RLS
applied; trying to insert events for multiple FIDs sequentially would fail unless each insert
is wrapped in `withFIDContext`. Using `edin_migrator` (BYPASSRLS) for the migration batch
insert is simpler and correct for a one-time administrative operation.

**Files to Create:**
- `edin-backend/cmd/migrate-commander-data/main.go`

**Tests (write first):**
```go
func TestMigration_DryRun_NoDataWritten(t *testing.T)
func TestMigration_SmallDataset_AllRowsMigrated(t *testing.T)
func TestMigration_DuplicateRows_Deduplicated(t *testing.T)
func TestMigration_InvalidEventType_SkippedAndLogged(t *testing.T)  // Go allowlist enforcement
func TestMigration_ReportsCorrectCounts(t *testing.T)
```

**Acceptance Criteria:**
- Dry-run reports without writing
- Row counts match (minus rejected events) after migration
- FID, timestamp, event_type, event_data all preserved

---

### Story 9.4 — Decommission Python EDIN Backend

**Complexity:** S

After validating Go backend is live and edin-client is successfully syncing:
1. Stop Python backend containers (do not delete yet — keep for 2 weeks)
2. Add `ARCHIVED.md` to `~/src/edin/edin-backend/` noting date and reason
3. Update `~/src/edin/CLAUDE.md` to mark Python backend as archived
4. After 2-week grace period with no issues: remove from process manager

**Acceptance Criteria:**
- Python backend containers are stopped and not auto-restarted
- All traffic confirmed going to Go backend (Caddy access logs)
- edin-client healthcheck confirms Go backend

---

### Story 9.5 — Observability for Copilot & Commander Data

**Complexity:** M

**Why:** The project CLAUDE.md has explicit observability requirements. The plan had no monitoring
story — this is a gap that would surface as an on-call surprise in production.

**What:**
- Prometheus metrics for all new endpoints:
  - `edin_ingest_events_total{fid_hash, status}` — counter (hash FID for privacy)
  - `edin_ingest_latency_seconds` — histogram
  - `edin_commander_auth_attempts_total{outcome}` — counter (success/failure/capi_timeout)
  - `edin_copilot_chat_sessions_active` — gauge
  - `edin_copilot_tool_calls_total{tool_name}` — counter
- Alerts:
  - CAPI timeout rate > 10% over 5min window → warning alert
  - Ingest error rate > 5% over 5min → warning alert
  - Redis PKCE state count > 500 → warning (approaching DoS threshold)
  - RLS policy violation error in DB logs → critical (should never happen)
- Dashboard: commander data volume (events/day by FID count bucket, not individual FIDs)

**Files to Modify:**
- `edin-backend/internal/httpapi/commander_ingest.go` — add metrics instrumentation
- `edin-backend/internal/httpapi/commander_auth.go` — add metrics
- `edin-backend/internal/httpapi/copilot_chat.go` — add session gauge
- `atlas/ansible/` or `atlas/terraform/` — alert rules

**Tests (write first):**
```go
func TestIngestHandler_EmitsMetricsOnSuccess(t *testing.T)
func TestIngestHandler_EmitsMetricsOnFailure(t *testing.T)
func TestAuthCallback_EmitsCAPITimeoutMetric(t *testing.T)
```

**Acceptance Criteria:**
- All new endpoints appear in Prometheus metrics output
- CAPI timeout alert fires in staging when CAPI mock returns timeout
- No FID or commander name in metric labels (privacy)

---

## Test Strategy Summary

### Go Backend Tests

| Layer | Test Type | Runner | Notes |
|---|---|---|---|
| Unit (auth, JWT, tools) | Mock-based | `go test ./...` | Fast |
| Repository | Integration (testcontainers, pinned TimescaleDB image) | `go test ./internal/store/...` | Requires Docker |
| HTTP handlers | `httptest.Server` | `go test ./internal/httpapi/...` | Redis mock or test Redis |
| End-to-end | Real containers + real WS | `go test -tags=integration ./...` | CI only |

### Frontend Tests

| Layer | Runner |
|---|---|
| Auth context, hooks | Vitest + MSW |
| Page components | Vitest + testing-library |

### Flutter Client Tests

| Layer | Runner |
|---|---|
| Services | `flutter test` |
| Integration | `flutter test --tags=integration` (against local Go backend) |

### Mandatory CI Gates

Every PR must pass:
- `go test ./...` (unit + handler tests)
- `go test -tags=integration ./internal/store/...` (RLS tests with real TimescaleDB)
- `npm run test` (frontend)
- `flutter test` (client)
- `flutter analyze`

**Mandatory test — cannot be skipped or marked flaky:**
`TestCommanderRepo_PoolReuse_NoCrossContamination` with `MaxConns=1` pool
`TestCommanderRepo_SetLocal_ExpiresAfterCommit` (would catch SET SESSION misuse)
`TestMigrateCommanderSchema_CompressionPolicyApplied` (would catch space-partition regression)

---

## Definition of Done (per story)

1. Tests specified in the story are written and **failing** before implementation begins (red)
2. Implementation written until all tests pass (green)
3. No new `go vet` warnings; `flutter analyze` clean
4. No hardcoded secrets, URLs, or FIDs in committed code
5. Sensitive error details not leaked in HTTP responses
6. Story's acceptance criteria verified

---

## Sequencing & Dependencies

```
Epic 0 (testutil, keygen) ─────────────────────────────────────────┐
                                                                     ↓
Epic 1 (Stories 1.1 + 1.2 together, same PR) ─── parallel ──── Epic 2 (Commander Auth)
                                                                     ↓
                                                            Epic 3 (Schema + Repo)
                                                            ↓               ↓
                                                     Epic 4 (Ingest)  Epic 5 (Query + Tools)
                                                                     ↓
                                                      Epic 6 (Copilot Chat)
                                                      Epic 7 (Frontend)     ← parallel
                                                      Epic 8 (edin-client)  ← parallel
                                                                     ↓
                                               Epic 9 (Infra + Migration + Decommission)
```

Epics 4 and 5 can be worked in parallel after Epic 3 completes.
Epics 6, 7, 8 can be worked in parallel after Epic 5 completes.
Epic 9 is last — decommission only after everything is validated in production.
Stories 1.1 and 1.2 **must land in the same PR**.

---

## Out of Scope

- Real-time push of events from server to copilot (events fetched when LLM calls tools)
- Mobile version of the copilot (web + desktop only)
- Multi-commander support in a single session
- Sharing copilot sessions between commanders
- Historical analytics / dashboards (infrastructure supports it; UI is future work)
- Kaine portal feature changes beyond Epic 1 security fixes
- Python backend LLM endpoints, admin endpoints, agent profiles (dropped, not ported)

---

## Appendix: Review Findings Applied

Full list of the 24 findings from the plan red-team, and where each was addressed:

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | Critical | Space partitioning + compression incompatible in TimescaleDB | Removed `by_hash` from Story 3.1; added test to confirm |
| 2 | Critical | RLS SET LOCAL test logical error; SET SESSION misuse would pass tests | Fixed Story 3.1 test spec; Story 3.2 adds SET SESSION detection test; pool pinned to MaxConns=1 |
| 3 | Critical | `/api/kaine/token` re-introduces XSS risk | Story 1.2: single-use nonce, CSRF custom header, 10s TTL |
| 4 | Critical | `betaToolDefsForContext` missing ScopeCopilotChat → empty tool list | Story 6.2: added `runner.go` to Files to Modify; added tests |
| 5 | Critical | Frontier OAuth scope wrong (`auth capi` not `openid`) | Story 2.1 config; Story 2.3 initiate handler; test added |
| 6 | Critical | CAPI unavailability causes auth failure (502) | Story 2.3: graceful fallback, placeholder name, `capi_pending` flag |
| 7 | High | WebSocket reconnect token-fetch race on reconnect | Story 1.1: 3s fetch timeout; no reconnect on 401 |
| 8 | High | `withFIDContext` deferred rollback ignores errors | Story 3.2: explicit defer with error logging |
| 9 | High | Rate limiter keyed on full auth header; rotation bypasses per-FID limit | Story 4.1: separate `ingestRateLimiter` using FID from context |
| 10 | High | Refresh flow underspecified (Redis schema, Frontier refresh token) | Story 2.5: full Redis key schema documented |
| 11 | High | Migration SQL creates roles with shell variable syntax (invalid SQL) | Story 3.1: removed role creation from SQL; moved to Story 9.2 Ansible |
| 12 | High | GDPR delete fails on compressed TimescaleDB chunks | Story 3.2 repo + Story 5.1 endpoint: decompress before delete; async 202 |
| 13 | High | Poll-based auth session fixation risk | Story 8.1: single-use poll; 410 after completion; crypto/rand session ID |
| 14 | Medium | Stories 1.1 and 1.2 have hard dependency; must be same PR | Sequencing note added; merged into single PR requirement |
| 15 | Medium | SameSite=Strict breaks top-level navigation from external links | All cookies changed to SameSite=Lax; security architecture table updated |
| 16 | Medium | `describe_tool` reveals tool names despite prompt hiding them | Story 6.1: accepted design tension documented explicitly |
| 17 | Medium | FID mismatch policy contradictory (400 vs log+continue) | Story 4.1 and Wire Format section: log+continue, not 400 |
| 18 | Medium | Testcontainers uses wrong image (no TimescaleDB extension) | Story 0.1: pin production image; require extension creation; add test |
| 19 | Medium | `fidFromContext` panic conflicts with project error handling rules | Story 2.4: returns `(string, error)` not panic |
| 20 | Medium | Migration script superuser availability undefined | Story 9.3: `edin_migrator` role with BYPASSRLS specified |
| 21 | Low | RSA key size not validated at load time | Story 0.2: LoadPrivateKey rejects < 2048 bits |
| 22 | Low | `/api/copilot/chat/ws` missing from Caddy story | Story 9.1: WebSocket proxy explicitly noted |
| 23 | Low | Cookie name not cross-referenced in Story 2.4 | Story 2.4: `commander_session` cookie name explicit |
| 24 | Low | No monitoring/observability story | Story 9.5 added |
