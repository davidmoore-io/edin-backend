# Auth Cleanup: Two-Environment Frontier-Only Auth

**Date:** 2026-04-15
**Status:** Approved
**Scope:** edin-client (Flutter), edin-backend (Go)

## Problem

The edin-client auth code carries 600+ lines of dead Authentik/Python-backend code across four environments, three of which no longer have a functioning backend. The `UserProfile` model holds Authentik-era fields. Token validation on startup calls an endpoint (`/api/v1/auth/me`) that doesn't exist in the Go backend, forcing re-login every launch.

## Decision

Strip auth down to a single strategy (Frontier PKCE poll-based) with two environments (development, production). Delete all Authentik code. Add `GET /api/v1/auth/me` to the Go backend.

## Environments

| Name | Base URL | When available |
|------|----------|----------------|
| Development | `https://edin-dev.crossmoore.io.ngrok.app` | Debug builds (default) |
| Production | `https://edin.space` | All builds (default in release) |

Debug builds render a dropdown to select either environment. Release builds lock to Production with no UI selector.

Both environments use identical code paths. The only difference is the base URL.

## edin-client Changes

### File structure

The current 1120-line `auth_service.dart` monolith is replaced by focused files:

| File | Responsibility | Approx size |
|------|---------------|-------------|
| `lib/models/commander_profile.dart` | `CommanderProfile` data class | ~30 lines |
| `lib/models/edin_environment.dart` | `EDINEnvironment` enum with URLs | ~25 lines |
| `lib/services/auth_service.dart` | Auth state, token storage, startup validation, logout | ~200 lines |
| `lib/services/frontier_auth.dart` | PKCE flow: initiate, poll, activate | ~100 lines |
| `lib/widgets/auth_widget.dart` | Login UI, env dropdown (debug only) | ~100 lines |

### Models

**`CommanderProfile`** replaces `UserProfile`. Fields:

- `fid` (String) — Frontier ID, e.g. `"F12345"`
- `commanderName` (String) — e.g. `"Pattern State"`
- `tokenExpiry` (DateTime) — JWT expiration

No `sub`, `email`, `groups`, `permissions`, `frontierProfile`, `customerId`. These were Authentik-specific and have no equivalent in the Go backend JWT.

**`EDINEnvironment`** enum:

```dart
enum EDINEnvironment {
  development('Development', 'https://edin-dev.crossmoore.io.ngrok.app'),
  production('Production', 'https://edin.space');

  const EDINEnvironment(this.displayName, this.baseUrl);
  final String displayName;
  final String baseUrl;
}
```

No `authentikUrl`. No `AuthStrategy` enum (only one strategy exists).

### Auth flow

Single code path for both environments:

1. `POST {baseUrl}/api/v1/auth/frontier/initiate` (no body, no auth headers)
   - Response: `{"auth_url": "https://auth.frontierstore.net/auth?...", "session_id": "uuid"}`
2. Open `auth_url` in system browser via `url_launcher`
3. Poll `GET {baseUrl}/api/v1/auth/frontier/poll?session_id={id}` every 3 seconds
   - 202: pending, keep polling
   - 200: `{"status": "complete", "token": "<jwt>"}` — done
   - 410: expired/consumed — abort with error
   - Timeout after 5 minutes
4. Decode JWT payload (base64, no verification needed client-side) to extract `fid` and `name` claims
5. Store token + profile in `FlutterSecureStorage`
6. Emit authenticated state

### Startup token validation

On launch, if a stored token exists:

1. `GET {baseUrl}/api/v1/auth/me` with `Authorization: Bearer <token>`
2. 200: token is valid, reconstruct `CommanderProfile` from response, proceed as authenticated
3. 401 or network error: clear stored token, show login screen

### Token refresh

The Go backend's `POST /api/v1/auth/refresh` accepts the current JWT (via Bearer header) and returns a new JWT. The client calls this when the token is approaching expiry (e.g. within 1 hour of `tokenExpiry`). No refresh token is stored — the existing JWT is the credential for refresh.

### Logout

1. Clear `FlutterSecureStorage` keys (token, profile, environment)
2. Emit unauthenticated state
3. Best-effort `POST {baseUrl}/api/v1/auth/logout` (fire-and-forget, non-blocking)

Note: `/api/v1/auth/logout` does not exist in the Go backend yet. The client treats logout as a local-only operation. If the endpoint is added later (for server-side JTI revocation), the client is already wired to call it.

### Secure storage keys

| Key | Value |
|-----|-------|
| `edin_token` | The EDIN JWT string |
| `edin_fid` | Commander FID |
| `edin_commander_name` | Commander name |
| `edin_environment` | Enum name (`development` or `production`) |

Renamed from `edin_authentik_token`, `edin_associated_fid`, etc. to remove Authentik naming.

### Deleted code

All of the following is removed entirely:

- `AuthStrategy` enum
- `authentikUrl` field on `EDINEnvironment`
- `local`, `ngrokDev`, `playground` environments
- `UserProfile` class
- `_loginWithAuthentik` and all `/api/v1/auth/frontend/*` calls
- `_handleOAuthCallbackUri`, `_waitForOAuth2Callback`, `_setupProtocolListeners`
- `_exchangeCodeForToken` (Authentik code exchange)
- `_fetchUserInfo` (Authentik userinfo)
- Deep-link protocol handling (`uni_links` usage)
- Any imports that become unused after removal

### Dependency changes

- Remove `uni_links` from `pubspec.yaml` if no other code uses it (deep-link handling was Authentik-only)
- `flutter_secure_storage`, `http`, `url_launcher`, `logging` remain

## edin-backend Changes

### New endpoint: `GET /api/v1/auth/me`

**Purpose:** Validate a stored commander JWT and return the commander's identity. Called by the client on startup to check whether a stored token is still valid.

**Handler:** `handleAuthMe` in `commander_client_auth.go`

**Auth:** Uses existing `withCommanderAuth` middleware (same as ingest/query routes). The middleware validates the JWT signature and expiry, extracts FID into context.

**Response (200):**

```json
{
  "fid": "F12345",
  "commander_name": "Pattern State"
}
```

Commander name is looked up from the JWT `name` claim (set at token issuance from Frontier `/me` response).

**Error responses:**

- 401: Missing, invalid, or expired JWT (handled by middleware)

**Route registration:** Add to `RegisterClientAuthRoutes` in `commander_client_auth.go`:

```go
mux.Handle("GET /api/v1/auth/me", s.withCommanderAuth(http.HandlerFunc(s.handleAuthMe)))
```

**Tests:**

```go
func TestAuthMe_ValidJWT_ReturnsFIDAndName(t *testing.T)
func TestAuthMe_ExpiredJWT_Returns401(t *testing.T)
func TestAuthMe_MissingAuth_Returns401(t *testing.T)
```

### No other backend changes

The existing endpoints are correct:
- `POST /api/v1/auth/frontier/initiate` — already registered
- `GET /api/v1/auth/frontier/poll` — already registered
- `POST /api/v1/auth/refresh` — already registered

## What is NOT in scope

- Adding `/api/v1/auth/logout` to the Go backend (JTI revocation) — future work
- Changes to `edin_api_service.dart` beyond updating imports — ingest/query endpoints are a separate concern
- Changes to the `settings_service.dart` `dataServerUrl` persistence — `auth_service.dart` will write the selected environment's URL there for `edin_api_service.dart` to read, same as today
- Frontier redirect URI registration — manual step on Frontier dev portal, not code
