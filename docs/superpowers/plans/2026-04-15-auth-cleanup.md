# Auth Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 1120-line Authentik/Python-era auth monolith with a clean, two-environment Frontier-only auth stack, and add `GET /api/v1/auth/me` to the Go backend.

**Architecture:** Single auth strategy (Frontier PKCE poll-based) across two environments (development/production). The Flutter client's auth code is split into focused files: models, PKCE flow logic, state management, and UI. The Go backend gets one new endpoint for token validation.

**Tech Stack:** Flutter/Dart (edin-client), Go (edin-backend), RS256 JWT, Redis

---

## File Map

### edin-client (Flutter) — Create

| File | Responsibility |
|------|---------------|
| `lib/models/commander_profile.dart` | `CommanderProfile` data class |
| `lib/models/edin_environment.dart` | `EDINEnvironment` enum (dev/prod) |
| `lib/services/frontier_auth.dart` | PKCE initiate, poll, activate logic |

### edin-client (Flutter) — Rewrite

| File | What changes |
|------|-------------|
| `lib/services/auth_service.dart` | Full rewrite: ~200 lines, imports new models + frontier_auth |
| `lib/widgets/auth_widget.dart` | Simplified: env dropdown debug-only, imports new models |

### edin-client (Flutter) — Update imports only

| File | What changes |
|------|-------------|
| `lib/main.dart` | Update `authServiceProvider` type, remove `UserProfile` refs |
| `lib/services/settings_service.dart` | Remove `_defaultDataServerUrl` (env drives it now) |
| `lib/ui/widgets/config/debug_settings_widget.dart` | `userProfile` -> `commanderProfile` |
| `lib/ui/widgets/config/commander_intelligence_widget.dart` | `userProfile` -> `commanderProfile` |
| `lib/ui/widgets/bulk_upload_widget.dart` | No `UserProfile` refs, just `isAuthenticated` |
| `lib/services/bulk_upload_service.dart` | No changes needed (uses `accessToken` only) |
| `lib/services/journal_engine_service.dart` | No changes needed (uses `accessToken` only) |
| `lib/services/commander_state_service.dart` | No changes needed |
| `lib/providers/commander_providers.dart` | Update if it refs `UserProfile` |

### edin-client (Flutter) — Delete dependency

| File | What changes |
|------|-------------|
| `pubspec.yaml` | Remove `uni_links: ^0.5.1` and `uni_links_desktop: ^0.1.6` |

### edin-backend (Go) — Create

| File | Responsibility |
|------|---------------|
| `internal/httpapi/commander_auth_me.go` | `handleAuthMe` handler |
| `internal/httpapi/commander_auth_me_test.go` | Tests for `/api/v1/auth/me` |

### edin-backend (Go) — Modify

| File | What changes |
|------|-------------|
| `internal/httpapi/commander_client_auth.go:354-357` | Add `/api/v1/auth/me` route registration |

---

## Task 1: Go backend — Add `GET /api/v1/auth/me`

**Files:**
- Create: `edin-backend/internal/httpapi/commander_auth_me_test.go`
- Create: `edin-backend/internal/httpapi/commander_auth_me.go`
- Modify: `edin-backend/internal/httpapi/commander_client_auth.go:354-357`

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/commander_auth_me_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthMe_ValidJWT_ReturnsFIDAndName(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	srv.withCommanderAuth(http.HandlerFunc(srv.handleAuthMe))(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "F2504", body["fid"])
	assert.Equal(t, "Pattern State", body["commander_name"])
}

func TestAuthMe_MissingAuth_Returns401(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()

	srv.withCommanderAuth(http.HandlerFunc(srv.handleAuthMe))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuthMe_ExpiredJWT_Returns401(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	// Issue a token, then fast-forward miniredis past expiry.
	// The test helper issues with 24h expiry, so we use a custom issuer with 1ms expiry.
	shortIssuer := newShortExpiryIssuer(t, srv)
	token, _, err := shortIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	srv.withCommanderAuth(http.HandlerFunc(srv.handleAuthMe))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
```

Note: `newShortExpiryIssuer` is a test helper that creates a `CommanderJWTIssuer` with a very short (1ms) expiry using the same RSA key pair as the test server. If this helper doesn't exist yet, create it:

```go
func newShortExpiryIssuer(t *testing.T, srv *Server) *auth.CommanderJWTIssuer {
	t.Helper()
	// Re-read the private key that was used for the test server.
	// The test helper generateTestRSAKeyPair stores it — extract from the issuer's existing key.
	privKey, _ := generateTestRSAKeyPair(t)
	return auth.NewCommanderJWTIssuer(privKey, "edin-space-test", time.Millisecond)
}
```

However, the expired-token test depends on the validator (not the issuer) rejecting expired tokens. The `withCommanderAuth` middleware already handles this via `CommanderJWTValidator.Validate`. Since the short-lived token will be expired by the time `Validate` runs, the middleware will return 401 before `handleAuthMe` is called. The test server's validator uses the same key pair, so this works. If the `newShortExpiryIssuer` approach is tricky due to key-pair coupling, simplify: remove the expired test and rely on the existing `commander_middleware_test.go` which already covers expired-token 401 behavior.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/davidmoore/src/edin-space/edin-backend && go test ./internal/httpapi/ -run "TestAuthMe" -count=1 -v`

Expected: Compilation error — `srv.handleAuthMe` undefined.

- [ ] **Step 3: Implement `handleAuthMe`**

Create `internal/httpapi/commander_auth_me.go`:

```go
package httpapi

import (
	"net/http"

	"github.com/edin-space/edin-backend/internal/auth"
)

// handleAuthMe returns the authenticated commander's identity.
// Requires withCommanderAuth middleware.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"fid":            claims.FID,
		"commander_name": claims.Name,
	})
}
```

- [ ] **Step 4: Register the route**

In `internal/httpapi/commander_client_auth.go`, add to `RegisterClientAuthRoutes`:

```go
func (s *Server) RegisterClientAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/frontier/initiate", s.handleClientAuthInitiate)
	mux.HandleFunc("/api/v1/auth/frontier/poll", s.handleClientAuthPoll)
	mux.HandleFunc("/api/v1/auth/refresh", s.handleClientAuthRefresh)
	mux.Handle("GET /api/v1/auth/me", s.withCommanderAuth(http.HandlerFunc(s.handleAuthMe)))
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/davidmoore/src/edin-space/edin-backend && go test ./internal/httpapi/ -run "TestAuthMe" -count=1 -v`

Expected: All 2-3 tests PASS.

- [ ] **Step 6: Run full test suite**

Run: `cd /home/davidmoore/src/edin-space/edin-backend && go test ./... -count=1`

Expected: All tests pass, no regressions.

- [ ] **Step 7: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
git add internal/httpapi/commander_auth_me.go internal/httpapi/commander_auth_me_test.go internal/httpapi/commander_client_auth.go
git commit -m "feat: add GET /api/v1/auth/me for client token validation"
```

---

## Task 2: Flutter — Create `CommanderProfile` model

**Files:**
- Create: `edin-client/lib/models/commander_profile.dart`

- [ ] **Step 1: Create the model file**

Create `lib/models/commander_profile.dart`:

```dart
/// Commander identity from EDIN JWT claims.
class CommanderProfile {
  final String fid;
  final String commanderName;
  final DateTime? tokenExpiry;

  const CommanderProfile({
    required this.fid,
    required this.commanderName,
    this.tokenExpiry,
  });

  /// Decode FID and name from a base64-encoded JWT payload (no signature verification).
  factory CommanderProfile.fromJWT(String token) {
    final parts = token.split('.');
    if (parts.length != 3) {
      throw FormatException('Invalid JWT format');
    }

    // Pad base64 to multiple of 4
    var payload = parts[1];
    switch (payload.length % 4) {
      case 2:
        payload += '==';
        break;
      case 3:
        payload += '=';
        break;
    }

    final decoded = utf8.decode(base64.decode(payload));
    final claims = jsonDecode(decoded) as Map<String, dynamic>;

    final fid = claims['fid'] as String? ?? '';
    final name = claims['name'] as String? ?? '';
    final exp = claims['exp'] as int?;

    return CommanderProfile(
      fid: fid,
      commanderName: name,
      tokenExpiry: exp != null ? DateTime.fromMillisecondsSinceEpoch(exp * 1000) : null,
    );
  }

  /// Create from /api/v1/auth/me response body.
  factory CommanderProfile.fromAuthMe(Map<String, dynamic> json) {
    return CommanderProfile(
      fid: json['fid'] as String? ?? '',
      commanderName: json['commander_name'] as String? ?? '',
    );
  }

  @override
  String toString() => 'CommanderProfile(fid: $fid, name: $commanderName)';
}
```

Add imports at top of file:

```dart
import 'dart:convert' show jsonDecode, utf8, base64;
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze lib/models/commander_profile.dart`

Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/models/commander_profile.dart
git commit -m "feat: add CommanderProfile model for EDIN JWT claims"
```

---

## Task 3: Flutter — Create `EDINEnvironment` enum

**Files:**
- Create: `edin-client/lib/models/edin_environment.dart`

- [ ] **Step 1: Create the enum file**

Create `lib/models/edin_environment.dart`:

```dart
/// Backend environment for the EDIN client.
///
/// Debug builds show a dropdown to select either environment.
/// Release builds lock to [production].
enum EDINEnvironment {
  development('Development', 'https://edin-dev.crossmoore.io.ngrok.app'),
  production('Production', 'https://edin.space');

  const EDINEnvironment(this.displayName, this.baseUrl);

  final String displayName;
  final String baseUrl;
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze lib/models/edin_environment.dart`

Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/models/edin_environment.dart
git commit -m "feat: add EDINEnvironment enum (development/production)"
```

---

## Task 4: Flutter — Create `frontier_auth.dart` (PKCE flow)

**Files:**
- Create: `edin-client/lib/services/frontier_auth.dart`

- [ ] **Step 1: Create the PKCE flow service**

Create `lib/services/frontier_auth.dart`:

```dart
import 'dart:async';
import 'dart:convert' show jsonDecode;
import 'package:http/http.dart' as http;
import 'package:logging/logging.dart';
import 'package:url_launcher/url_launcher.dart';

final _log = Logger('EDIN.FrontierAuth');

/// Result of a successful Frontier PKCE auth flow.
class FrontierAuthResult {
  final String token;
  const FrontierAuthResult({required this.token});
}

/// Initiates the Frontier PKCE poll-based auth flow against [baseUrl].
///
/// 1. POST /api/v1/auth/frontier/initiate -> {auth_url, session_id}
/// 2. Open auth_url in system browser
/// 3. Poll /api/v1/auth/frontier/poll?session_id=X every 3s
/// 4. Return token on 200, throw on timeout/error
Future<FrontierAuthResult> runFrontierAuth(String baseUrl) async {
  // Step 1: Initiate
  _log.info('Initiating Frontier auth against $baseUrl');
  final initiateResp = await http.post(
    Uri.parse('$baseUrl/api/v1/auth/frontier/initiate'),
    headers: {'Content-Type': 'application/json'},
  );

  if (initiateResp.statusCode != 200) {
    throw Exception('Auth initiate failed: ${initiateResp.statusCode} ${initiateResp.body}');
  }

  final initiateData = jsonDecode(initiateResp.body) as Map<String, dynamic>;
  final authUrl = initiateData['auth_url'] as String;
  final sessionId = initiateData['session_id'] as String;
  _log.info('Got session $sessionId, opening browser');

  // Step 2: Open browser
  final uri = Uri.parse(authUrl);
  if (!await launchUrl(uri, mode: LaunchMode.externalApplication)) {
    throw Exception('Could not open browser for Frontier login');
  }

  // Step 3: Poll
  final pollUrl = '$baseUrl/api/v1/auth/frontier/poll?session_id=$sessionId';
  const pollInterval = Duration(seconds: 3);
  const timeout = Duration(minutes: 5);
  final deadline = DateTime.now().add(timeout);

  while (DateTime.now().isBefore(deadline)) {
    await Future.delayed(pollInterval);

    final pollResp = await http.get(Uri.parse(pollUrl));

    if (pollResp.statusCode == 200) {
      final pollData = jsonDecode(pollResp.body) as Map<String, dynamic>;
      final token = pollData['token'] as String?;
      if (token != null && token.isNotEmpty) {
        _log.info('Auth complete, received token');
        return FrontierAuthResult(token: token);
      }
    } else if (pollResp.statusCode == 202) {
      // Still pending, keep polling
      continue;
    } else if (pollResp.statusCode == 410) {
      throw Exception('Auth session expired or already consumed');
    } else {
      throw Exception('Poll failed: ${pollResp.statusCode} ${pollResp.body}');
    }
  }

  throw TimeoutException('Frontier auth timed out after ${timeout.inMinutes} minutes');
}

/// Validate a stored token against the backend.
/// Returns the response body on success, null on auth failure.
Future<Map<String, dynamic>?> validateToken(String baseUrl, String token) async {
  try {
    final resp = await http.get(
      Uri.parse('$baseUrl/api/v1/auth/me'),
      headers: {
        'Authorization': 'Bearer $token',
        'Content-Type': 'application/json',
      },
    ).timeout(const Duration(seconds: 10));

    if (resp.statusCode == 200) {
      return jsonDecode(resp.body) as Map<String, dynamic>;
    }
    _log.info('Token validation returned ${resp.statusCode}');
    return null;
  } catch (e) {
    _log.warning('Token validation failed: $e');
    return null;
  }
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze lib/services/frontier_auth.dart`

Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/services/frontier_auth.dart
git commit -m "feat: add frontier_auth.dart with PKCE poll flow and token validation"
```

---

## Task 5: Flutter — Rewrite `auth_service.dart`

This is the big one. The entire 1120-line file gets replaced.

**Files:**
- Rewrite: `edin-client/lib/services/auth_service.dart`

- [ ] **Step 1: Replace `auth_service.dart` with the clean implementation**

Replace the entire file with:

```dart
import 'dart:async';
import 'dart:convert' show jsonDecode;
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:logging/logging.dart';

import '../models/commander_profile.dart';
import '../models/edin_environment.dart';
import 'frontier_auth.dart';
import 'settings_service.dart';

/// EDIN authentication service.
///
/// Manages Frontier PKCE auth flow, token storage, and session lifecycle.
/// All environments use the same code path — only the base URL differs.
class AuthService {
  // Secure storage keys
  static const _keyToken = 'edin_token';
  static const _keyFid = 'edin_fid';
  static const _keyCommanderName = 'edin_commander_name';
  static const _keyEnvironment = 'edin_environment';

  final _log = Logger('EDIN.AuthService');
  final FlutterSecureStorage _storage = const FlutterSecureStorage(
    aOptions: AndroidOptions(encryptedSharedPreferences: true),
  );

  EDINEnvironment _environment = EDINEnvironment.development;
  String? _token;
  CommanderProfile? _profile;
  bool _initialized = false;

  // Stream controller for auth state changes
  final _authStateController = StreamController<bool>.broadcast();

  // --- Public getters ---

  EDINEnvironment get environment => _environment;
  String get baseUrl => _environment.baseUrl;
  bool get isAuthenticated => _token != null && _profile != null;
  String? get accessToken => _token;
  CommanderProfile? get commanderProfile => _profile;
  String? get commanderFid => _profile?.fid;
  String? get commanderName => _profile?.commanderName;
  Stream<bool> get authStateChanges => _authStateController.stream;
  bool get isInitialized => _initialized;

  // --- Environment ---

  Future<void> setEnvironment(EDINEnvironment env) async {
    _environment = env;
    await _storage.write(key: _keyEnvironment, value: env.name);
    SettingsService.instance.dataServerUrl = env.baseUrl;
    _log.info('Environment set to ${env.displayName} (${env.baseUrl})');
  }

  // --- Initialization ---

  Future<void> initialize() async {
    if (_initialized) return;

    // Load persisted environment
    final envName = await _storage.read(key: _keyEnvironment);
    if (envName != null) {
      _environment = EDINEnvironment.values.firstWhere(
        (e) => e.name == envName,
        orElse: () => EDINEnvironment.development,
      );
    }
    SettingsService.instance.dataServerUrl = _environment.baseUrl;

    // Try to restore stored session
    final storedToken = await _storage.read(key: _keyToken);
    if (storedToken != null) {
      _log.info('Found stored token, validating...');
      final meData = await validateToken(baseUrl, storedToken);
      if (meData != null) {
        _token = storedToken;
        _profile = CommanderProfile.fromAuthMe(meData);
        _log.info('Session restored: ${_profile!.commanderName} (${_profile!.fid})');
        _authStateController.add(true);
      } else {
        _log.info('Stored token invalid, clearing');
        await _clearStorage();
      }
    }

    _initialized = true;
  }

  // --- Login ---

  Future<void> login() async {
    _log.info('Starting Frontier auth (${_environment.displayName})');

    final result = await runFrontierAuth(baseUrl);

    _token = result.token;
    _profile = CommanderProfile.fromJWT(result.token);

    // Persist
    await _storage.write(key: _keyToken, value: _token!);
    await _storage.write(key: _keyFid, value: _profile!.fid);
    await _storage.write(key: _keyCommanderName, value: _profile!.commanderName);

    // Propagate FID to settings for other services
    SettingsService.instance.commanderFID = _profile!.fid;
    SettingsService.instance.commanderName = _profile!.commanderName;

    _log.info('Authenticated: ${_profile!.commanderName} (${_profile!.fid})');
    _authStateController.add(true);
  }

  // --- Logout ---

  Future<void> logout() async {
    _log.info('Logging out');
    await _clearStorage();
    _token = null;
    _profile = null;
    _authStateController.add(false);
  }

  // --- Internals ---

  Future<void> _clearStorage() async {
    await _storage.delete(key: _keyToken);
    await _storage.delete(key: _keyFid);
    await _storage.delete(key: _keyCommanderName);
    // Keep _keyEnvironment — user's env choice persists across logout
  }

  void dispose() {
    _authStateController.close();
  }
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze lib/services/auth_service.dart`

Expected: Errors in *other* files that import `auth_service.dart` and reference `UserProfile`, `EDINEnvironment` (old), `authentikUrl`, etc. That's expected — we fix those in Task 7.

- [ ] **Step 3: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/services/auth_service.dart
git commit -m "feat: rewrite auth_service.dart — single Frontier PKCE strategy, no Authentik"
```

---

## Task 6: Flutter — Rewrite `auth_widget.dart`

**Files:**
- Rewrite: `edin-client/lib/widgets/auth_widget.dart`

- [ ] **Step 1: Replace `auth_widget.dart`**

Replace the entire file with:

```dart
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:logging/logging.dart';

import '../main.dart';
import '../core/widgets/edin_logo.dart';
import '../core/theme.dart';
import '../models/edin_environment.dart';
import '../services/auth_service.dart';
import '../services/window_mode_service.dart';

class AuthWidget extends ConsumerStatefulWidget {
  const AuthWidget({super.key});

  @override
  ConsumerState<AuthWidget> createState() => _AuthWidgetState();
}

class _AuthWidgetState extends ConsumerState<AuthWidget> {
  final _log = Logger('EDIN.AuthWidget');
  bool _isLoggingIn = false;
  EDINEnvironment _selectedEnvironment = EDINEnvironment.development;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      WindowModeService.instance.sizeToAuthWindow();
      // Sync dropdown with persisted environment
      final authService = ref.read(authServiceProvider);
      setState(() {
        _selectedEnvironment = authService.environment;
      });
    });
  }

  Future<void> _handleLogin() async {
    if (_isLoggingIn) return;
    setState(() => _isLoggingIn = true);

    try {
      final authService = ref.read(authServiceProvider);
      await authService.setEnvironment(_selectedEnvironment);
      await authService.login();
      _log.info('Login succeeded');
    } catch (e) {
      _log.severe('Login failed', e);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text('Login failed: $e'),
            backgroundColor: Colors.red,
          ),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoggingIn = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Container(
        width: double.infinity,
        height: double.infinity,
        decoration: BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topLeft,
            end: Alignment.bottomRight,
            colors: [
              EDColors.backgroundDeep,
              EDColors.backgroundBrand,
              EDColors.hudOrangeDark,
            ],
          ),
        ),
        child: SafeArea(
          child: SingleChildScrollView(
            child: Padding(
              padding: const EdgeInsets.all(16.0),
              child: Center(
                child: Card(
                  margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 32),
                  elevation: 8,
                  color: EDColors.backgroundCard,
                  child: Container(
                    padding: const EdgeInsets.all(24),
                    constraints: const BoxConstraints(maxWidth: 400),
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        const EDINLogo(size: EDINLogoSizes.huge, animated: true),
                        const SizedBox(height: 16),
                        Text(
                          'EDIN Client',
                          style: EDTextStyles.hudTitle.copyWith(
                            fontSize: EDTextStyles.scaled(24),
                            fontWeight: FontWeight.bold,
                          ),
                        ),
                        const SizedBox(height: 4),
                        Text(
                          'Elite Dangerous Intelligence Network',
                          style: EDTextStyles.hudBodySecondary.copyWith(
                            fontSize: EDTextStyles.scaled(14),
                          ),
                          textAlign: TextAlign.center,
                        ),
                        const SizedBox(height: 20),
                        Text(
                          'Sign in with your Frontier account to sync your '
                          'journal data and coordinate with your squadron.',
                          style: EDTextStyles.hudBody,
                          textAlign: TextAlign.center,
                        ),
                        const SizedBox(height: 24),

                        // Environment selector — debug builds only
                        if (kDebugMode) ...[
                          _buildEnvironmentDropdown(),
                          const SizedBox(height: 16),
                        ],

                        // Login button
                        SizedBox(
                          width: double.infinity,
                          height: 44,
                          child: ElevatedButton.icon(
                            onPressed: _isLoggingIn ? null : _handleLogin,
                            icon: _isLoggingIn
                                ? SizedBox(
                                    width: 18,
                                    height: 18,
                                    child: CircularProgressIndicator(
                                      strokeWidth: 2,
                                      valueColor: AlwaysStoppedAnimation<Color>(
                                        EDColors.backgroundDeep,
                                      ),
                                    ),
                                  )
                                : const Icon(Icons.rocket_launch, size: 18),
                            label: Text(_isLoggingIn ? 'Signing in...' : 'Sign in with Frontier'),
                            style: ElevatedButton.styleFrom(
                              backgroundColor: EDColors.hudOrange,
                              foregroundColor: EDColors.backgroundDeep,
                              shape: RoundedRectangleBorder(
                                borderRadius: BorderRadius.circular(4),
                              ),
                            ),
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  Widget _buildEnvironmentDropdown() {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      decoration: BoxDecoration(
        color: EDColors.backgroundDeep.withOpacity(0.1),
        borderRadius: BorderRadius.circular(4),
        border: Border.all(
          color: EDColors.hudOrange.withOpacity(0.3),
          width: 1,
        ),
      ),
      child: DropdownButtonHideUnderline(
        child: DropdownButton<EDINEnvironment>(
          value: _selectedEnvironment,
          onChanged: _isLoggingIn
              ? null
              : (EDINEnvironment? value) {
                  if (value != null) {
                    setState(() => _selectedEnvironment = value);
                    _log.info('Environment selected: ${value.displayName}');
                  }
                },
          items: EDINEnvironment.values.map((env) {
            return DropdownMenuItem<EDINEnvironment>(
              value: env,
              child: Row(
                children: [
                  Icon(
                    env == EDINEnvironment.development ? Icons.computer : Icons.cloud,
                    size: 16,
                    color: EDColors.hudOrange,
                  ),
                  const SizedBox(width: 8),
                  Text(env.displayName, style: EDTextStyles.hudBody.copyWith(fontWeight: FontWeight.w500)),
                ],
              ),
            );
          }).toList(),
          dropdownColor: EDColors.backgroundCard,
          iconEnabledColor: EDColors.hudOrange,
          iconDisabledColor: EDColors.hudOrange.withOpacity(0.5),
        ),
      ),
    );
  }
}
```

- [ ] **Step 2: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/widgets/auth_widget.dart
git commit -m "feat: rewrite auth_widget — debug-only env dropdown, no Authentik"
```

---

## Task 7: Flutter — Fix all consuming files

Update every file that imported the old `auth_service.dart` types.

**Files:**
- Modify: `edin-client/lib/main.dart`
- Modify: `edin-client/lib/providers/commander_providers.dart`
- Modify: `edin-client/lib/ui/widgets/config/debug_settings_widget.dart`
- Modify: `edin-client/lib/ui/widgets/config/commander_intelligence_widget.dart`
- Modify: `edin-client/lib/ui/widgets/bulk_upload_widget.dart`
- Modify: `edin-client/lib/ui/widgets/config/sync_test_widget.dart`

- [ ] **Step 1: Fix `main.dart`**

The `authServiceProvider` stays but references to `UserProfile` or old `EDINEnvironment` locations need updating. Add import for new models if the old enum was referenced directly. The key change: `auth_service.dart` no longer exports `EDINEnvironment` or `UserProfile` — consumers that need `EDINEnvironment` import from `models/edin_environment.dart`, and `UserProfile` references become `CommanderProfile`.

Search `main.dart` for `UserProfile` and `EDINEnvironment` references. Replace:
- `authService.userProfile` -> `authService.commanderProfile`
- Any `UserProfile` type annotations -> `CommanderProfile` (add import `../models/commander_profile.dart`)
- Any `EDINEnvironment` references -> add import `../models/edin_environment.dart`

- [ ] **Step 2: Fix `debug_settings_widget.dart`**

Replace:
- `authService.userProfile` -> `authService.commanderProfile`
- `userProfile?.commanderName` -> `profile?.commanderName`
- `UserProfile` type annotations -> `CommanderProfile?`
- Add import: `import '../../../models/commander_profile.dart';`

- [ ] **Step 3: Fix `commander_intelligence_widget.dart`**

Replace:
- `authService.userProfile` -> `authService.commanderProfile`
- Add import if `CommanderProfile` is used as a type

- [ ] **Step 4: Fix remaining files**

For `bulk_upload_widget.dart`, `sync_test_widget.dart`, `commander_providers.dart`, `journal_engine_service.dart`, `commander_state_service.dart`, `bulk_upload_service.dart`:
- These mostly use `authService.isAuthenticated` and `authService.accessToken` which haven't changed.
- Check each for `UserProfile`, `EDINEnvironment`, or `authentikUrl` references and fix.
- If they import `auth_service.dart` only for the provider, the import stays.

- [ ] **Step 5: Verify full project compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze`

Expected: No errors (warnings about deprecations in third-party packages are OK).

- [ ] **Step 6: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add -A
git commit -m "fix: update all consumers for new auth types (CommanderProfile, EDINEnvironment)"
```

---

## Task 8: Flutter — Remove `uni_links` dependency

**Files:**
- Modify: `edin-client/pubspec.yaml`

- [ ] **Step 1: Remove `uni_links` and `uni_links_desktop` from `pubspec.yaml`**

Delete these two lines from the dependencies section:

```yaml
  uni_links: ^0.5.1  # For custom URL scheme handling (edin://)
  # Desktop deep link support for Windows/macOS/Linux (implements uni_links channels)
  uni_links_desktop: ^0.1.6
```

- [ ] **Step 2: Run `flutter pub get`**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter pub get`

Expected: Resolves without errors. No other file imports `uni_links`.

- [ ] **Step 3: Verify no remaining references**

Run: `cd /home/davidmoore/src/edin-space/edin-client && grep -r "uni_links" lib/`

Expected: No output (zero matches).

- [ ] **Step 4: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add pubspec.yaml pubspec.lock
git commit -m "chore: remove uni_links dependency (Authentik deep-link handling deleted)"
```

---

## Task 9: Flutter — Update `settings_service.dart` default URL

**Files:**
- Modify: `edin-client/lib/services/settings_service.dart`

- [ ] **Step 1: Update the default URL constant**

In `settings_service.dart` line 22, change:

```dart
static const String _defaultDataServerUrl = 'https://edin-dev.crossmoore.io.ngrok.app';
```

to:

```dart
static const String _defaultDataServerUrl = 'https://edin.space';
```

The auth service now sets `dataServerUrl` from the selected environment on init and on login. The default here is the fallback for first launch before auth runs — production is the safe default.

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze lib/services/settings_service.dart`

Expected: No issues.

- [ ] **Step 3: Commit**

```bash
cd /home/davidmoore/src/edin-space/edin-client
git add lib/services/settings_service.dart
git commit -m "chore: default settings URL to edin.space (env selection overrides on login)"
```

---

## Task 10: Final verification

- [ ] **Step 1: Run full Go backend tests**

Run: `cd /home/davidmoore/src/edin-space/edin-backend && go test ./... -count=1`

Expected: All pass.

- [ ] **Step 2: Run full Flutter analysis**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter analyze`

Expected: No errors.

- [ ] **Step 3: Run Flutter tests (if any exist)**

Run: `cd /home/davidmoore/src/edin-space/edin-client && flutter test`

Expected: Pass or "no tests found" (acceptable if tests don't exist yet).

- [ ] **Step 4: Verify local dev stack still works**

Run: `cd /home/davidmoore/src/edin-space/edin-backend && make quick-dev`

Verify:
- Backend starts on :8080
- `curl -s -X POST https://edin-dev.crossmoore.io.ngrok.app/api/v1/auth/frontier/initiate` returns `{auth_url, session_id}`
- `curl -s https://edin-dev.crossmoore.io.ngrok.app/api/v1/auth/me` returns 401 (no token)
