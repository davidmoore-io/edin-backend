package config

import (
	"testing"
	"time"
)

// TestConfig_CommanderAuth_DefaultsApplied verifies all Commander auth defaults are applied
// when no env vars are set.
func TestConfig_CommanderAuth_DefaultsApplied(t *testing.T) {
	cfg := loadCommanderAuthConfig()

	if cfg.JWTIssuer != "edin-space" {
		t.Errorf("JWTIssuer: got %q, want %q", cfg.JWTIssuer, "edin-space")
	}
	if cfg.JWTExpiry != 24*time.Hour {
		t.Errorf("JWTExpiry: got %v, want %v", cfg.JWTExpiry, 24*time.Hour)
	}
	if cfg.FrontierScope != "auth capi" {
		t.Errorf("FrontierScope: got %q, want %q", cfg.FrontierScope, "auth capi")
	}
	if cfg.FrontierCAPITimeout != 10*time.Second {
		t.Errorf("FrontierCAPITimeout: got %v, want %v", cfg.FrontierCAPITimeout, 10*time.Second)
	}
	if cfg.PKCEStateTTL != 10*time.Minute {
		t.Errorf("PKCEStateTTL: got %v, want %v", cfg.PKCEStateTTL, 10*time.Minute)
	}
	if cfg.PKCEMaxPending != 1000 {
		t.Errorf("PKCEMaxPending: got %d, want %d", cfg.PKCEMaxPending, 1000)
	}
	if cfg.Enabled {
		t.Errorf("Enabled: got true, want false (no keys set)")
	}
}

func TestEDINConfigLoadsGalaxyReaderDSN(t *testing.T) {
	const dsn = "postgres://galaxy_reader:secret@eddn-timescaledb:5432/eddn_raw?sslmode=disable"
	t.Setenv("GALAXY_READER_DSN", dsn)

	cfg := loadEDINConfig()

	if cfg.GalaxyReaderDSN != dsn {
		t.Fatalf("GalaxyReaderDSN = %q, want %q", cfg.GalaxyReaderDSN, dsn)
	}
}

// TestConfig_CommanderAuth_DefaultScopeIsAuthCAPI explicitly verifies the FrontierScope
// default is "auth capi" and not "openid" or any other value.
func TestConfig_CommanderAuth_DefaultScopeIsAuthCAPI(t *testing.T) {
	cfg := loadCommanderAuthConfig()

	const want = "auth capi"
	if cfg.FrontierScope != want {
		t.Errorf("FrontierScope default: got %q, want %q — critical: wrong scope breaks Frontier OAuth2", cfg.FrontierScope, want)
	}
}

// TestConfig_CommanderAuth_DefaultCAPITimeout10s verifies FrontierCAPITimeout defaults to 10s.
func TestConfig_CommanderAuth_DefaultCAPITimeout10s(t *testing.T) {
	cfg := loadCommanderAuthConfig()

	const want = 10 * time.Second
	if cfg.FrontierCAPITimeout != want {
		t.Errorf("FrontierCAPITimeout: got %v, want %v", cfg.FrontierCAPITimeout, want)
	}
}

// TestConfig_CommanderAuth_MissingKeys_DisabledByDefault verifies that even when
// COMMANDER_AUTH_ENABLED=true, Enabled is false if key paths are empty.
func TestConfig_CommanderAuth_MissingKeys_DisabledByDefault(t *testing.T) {
	t.Setenv("COMMANDER_AUTH_ENABLED", "true")
	// Key paths intentionally not set — they remain empty.

	cfg := loadCommanderAuthConfig()

	if cfg.Enabled {
		t.Error("Enabled: got true, want false — must be disabled when key paths are empty, even if env var says true")
	}
}

// TestConfig_CommanderAuth_LoadsFromEnv verifies that env vars are picked up correctly.
func TestConfig_CommanderAuth_LoadsFromEnv(t *testing.T) {
	t.Setenv("COMMANDER_AUTH_ENABLED", "true")
	t.Setenv("COMMANDER_JWT_PRIVATE_KEY_PATH", "/etc/edin/private.pem")
	t.Setenv("COMMANDER_JWT_PUBLIC_KEY_PATH", "/etc/edin/public.pem")
	t.Setenv("COMMANDER_JWT_ISSUER", "edin-test")
	t.Setenv("COMMANDER_JWT_EXPIRY", "48h")
	t.Setenv("FRONTIER_CLIENT_ID", "test-client-id")
	t.Setenv("FRONTIER_CLIENT_SECRET", "test-client-secret")
	t.Setenv("FRONTIER_SCOPE", "auth capi extra")
	t.Setenv("FRONTIER_CAPI_TIMEOUT", "30s")
	t.Setenv("PKCE_STATE_TTL", "5m")
	t.Setenv("PKCE_MAX_PENDING", "500")
	t.Setenv("COMMANDER_COOKIE_DOMAIN", ".edin.space")
	t.Setenv("COMMANDER_COOKIE_SECURE", "true")

	cfg := loadCommanderAuthConfig()

	if !cfg.Enabled {
		t.Error("Enabled: got false, want true (both key paths provided)")
	}
	if cfg.PrivateKeyPath != "/etc/edin/private.pem" {
		t.Errorf("PrivateKeyPath: got %q, want %q", cfg.PrivateKeyPath, "/etc/edin/private.pem")
	}
	if cfg.PublicKeyPath != "/etc/edin/public.pem" {
		t.Errorf("PublicKeyPath: got %q, want %q", cfg.PublicKeyPath, "/etc/edin/public.pem")
	}
	if cfg.JWTIssuer != "edin-test" {
		t.Errorf("JWTIssuer: got %q, want %q", cfg.JWTIssuer, "edin-test")
	}
	if cfg.JWTExpiry != 48*time.Hour {
		t.Errorf("JWTExpiry: got %v, want %v", cfg.JWTExpiry, 48*time.Hour)
	}
	if cfg.FrontierClientID != "test-client-id" {
		t.Errorf("FrontierClientID: got %q, want %q", cfg.FrontierClientID, "test-client-id")
	}
	if cfg.FrontierClientSecret != "test-client-secret" {
		t.Errorf("FrontierClientSecret: got %q, want %q", cfg.FrontierClientSecret, "test-client-secret")
	}
	if cfg.FrontierScope != "auth capi extra" {
		t.Errorf("FrontierScope: got %q, want %q", cfg.FrontierScope, "auth capi extra")
	}
	if cfg.FrontierCAPITimeout != 30*time.Second {
		t.Errorf("FrontierCAPITimeout: got %v, want %v", cfg.FrontierCAPITimeout, 30*time.Second)
	}
	if cfg.PKCEStateTTL != 5*time.Minute {
		t.Errorf("PKCEStateTTL: got %v, want %v", cfg.PKCEStateTTL, 5*time.Minute)
	}
	if cfg.PKCEMaxPending != 500 {
		t.Errorf("PKCEMaxPending: got %d, want %d", cfg.PKCEMaxPending, 500)
	}
	if cfg.CookieDomain != ".edin.space" {
		t.Errorf("CookieDomain: got %q, want %q", cfg.CookieDomain, ".edin.space")
	}
	if !cfg.CookieSecure {
		t.Error("CookieSecure: got false, want true")
	}
	// Hardcoded values — verify they're unchanged.
	if cfg.CookieName != "commander_session" {
		t.Errorf("CookieName: got %q, want %q", cfg.CookieName, "commander_session")
	}
	if cfg.CookiePath != "/api/commander" {
		t.Errorf("CookiePath: got %q, want %q", cfg.CookiePath, "/api/commander")
	}
	if cfg.CookieMaxAge != 86400 {
		t.Errorf("CookieMaxAge: got %d, want %d", cfg.CookieMaxAge, 86400)
	}
}
