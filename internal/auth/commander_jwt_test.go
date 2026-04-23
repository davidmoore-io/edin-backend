package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRedis creates an in-memory Redis client backed by miniredis for testing.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// generateTestKeyPair returns a fresh 2048-bit RSA key pair for testing.
func generateTestKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key, &key.PublicKey
}

func TestCommanderJWT_Issue_ContainsFIDClaim(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Parse token to inspect claims.
	claims := &auth.CommanderClaims{}
	_, err = jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return pubKey, nil
	})
	require.NoError(t, err)
	require.Equal(t, "F2504", claims.FID)
}

func TestCommanderJWT_Issue_RS256Algorithm(t *testing.T) {
	privKey, _ := generateTestKeyPair(t)
	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Parse without verifying signature so we can inspect the header.
	token, _, err := new(jwt.Parser).ParseUnverified(tokenStr, &auth.CommanderClaims{})
	require.NoError(t, err)
	require.Equal(t, "RS256", token.Header["alg"])
}

func TestCommanderJWT_Issue_JTIIsNonEmpty(t *testing.T) {
	privKey, _ := generateTestKeyPair(t)
	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)

	_, jti, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)
	require.NotEmpty(t, jti)

	// Must match UUID format: 8-4-4-4-12 hex chars.
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	require.Regexp(t, uuidRe, jti)
}

func TestCommanderJWT_Validate_ValidToken_ReturnsClaims(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	claims, err := validator.Validate(context.Background(), tokenStr)
	require.NoError(t, err)
	require.NotNil(t, claims)
	require.Equal(t, "F2504", claims.FID)
	require.Equal(t, "Pattern State", claims.Name)
}

func TestCommanderJWT_Validate_ExpiredToken_ReturnsError(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	// Issue with negative expiry so the token is already expired.
	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", -time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	_, err = validator.Validate(context.Background(), tokenStr)
	require.Error(t, err)
}

func TestCommanderJWT_Validate_WrongIssuer_ReturnsError(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	// Validator expects a different issuer.
	validator := auth.NewCommanderJWTValidator(pubKey, "other", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	_, err = validator.Validate(context.Background(), tokenStr)
	require.Error(t, err)
}

func TestCommanderJWT_Validate_TamperedSignature_ReturnsError(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Corrupt the signature by replacing the entire signature part with garbage.
	// A JWT is header.payload.signature — replace the last segment.
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)
	// Replace signature with a fixed invalid value (valid base64url chars but wrong signature).
	tampered := parts[0] + "." + parts[1] + ".aW52YWxpZHNpZ25hdHVyZWhlcmUx"

	_, err = validator.Validate(context.Background(), tampered)
	require.Error(t, err)
}

func TestCommanderJWT_Validate_RevokedJTI_ReturnsError(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, jti, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Revoke the JTI before validation.
	err = validator.RevokeJTI(context.Background(), jti, time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = validator.Validate(context.Background(), tokenStr)
	require.Error(t, err)
	require.ErrorIs(t, err, auth.ErrTokenRevoked)
}

func TestCommanderJWT_Validate_DifferentPublicKey_ReturnsError(t *testing.T) {
	privKey1, _ := generateTestKeyPair(t)
	_, pubKey2 := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey1, "edin-space", time.Hour)
	// Validator uses a different public key than what was used to sign.
	validator := auth.NewCommanderJWTValidator(pubKey2, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	_, err = validator.Validate(context.Background(), tokenStr)
	require.Error(t, err)
}

func TestCommanderJWT_Roundtrip_FIDPreserved(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F9999", "Test Commander", nil)
	require.NoError(t, err)

	claims, err := validator.Validate(context.Background(), tokenStr)
	require.NoError(t, err)
	require.Equal(t, "F9999", claims.FID)
}

// TestCommanderJWT_RoundTripPreservesScopes asserts the scopes array survives
// a signature/issue/validate round-trip with the expected order.
func TestCommanderJWT_RoundTripPreservesScopes(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", []authz.Scope{
		authz.ScopeGalaxyRead,
		authz.ScopeCommanderData,
	})
	require.NoError(t, err)

	claims, err := validator.Validate(context.Background(), tokenStr)
	require.NoError(t, err)
	require.Equal(t, []string{"galaxy_read", "commander_data"}, claims.Scopes)
}

// TestCommanderJWT_EmptyScopesSurvivesRoundTrip asserts that an Issue with nil
// scopes yields a nil/empty Scopes on Validate. omitempty means the claim is
// simply absent from the JWT payload.
func TestCommanderJWT_EmptyScopesSurvivesRoundTrip(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	// nil scopes.
	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)
	claims, err := validator.Validate(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Empty(t, claims.Scopes)

	// Empty (non-nil) scopes behaves the same way.
	tokenStr, _, err = issuer.Issue("F2504", "Pattern State", []authz.Scope{})
	require.NoError(t, err)
	claims, err = validator.Validate(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Empty(t, claims.Scopes)
}

// TestCommanderJWT_TamperedScopesRejectedBySignature simulates an attacker
// modifying the "scopes" claim in the JWT payload. Validate must reject it
// with a signature error — the per-scope authorisation check depends on this.
func TestCommanderJWT_TamperedScopesRejectedBySignature(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)
	rdb := newTestRedis(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(pubKey, "edin-space", rdb)

	tokenStr, _, err := issuer.Issue("F2504", "Pattern State", []authz.Scope{authz.ScopeCopilotChat})
	require.NoError(t, err)

	// Split into header.payload.signature, tamper with the payload, re-encode.
	parts := strings.SplitN(tokenStr, ".", 3)
	require.Len(t, parts, 3)

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	// Escalate privileges: add admin scope that was never signed.
	payload["scopes"] = []string{"copilot_chat", "admin"}
	tampered, err := json.Marshal(payload)
	require.NoError(t, err)
	tamperedPayload := base64.RawURLEncoding.EncodeToString(tampered)
	tamperedToken := parts[0] + "." + tamperedPayload + "." + parts[2]

	_, err = validator.Validate(context.Background(), tamperedToken)
	require.Error(t, err, "tampered payload must fail signature verification")
	// The signing-key mismatch produces a parse-level error from jwt.ParseWithClaims.
	assert.Contains(t, err.Error(), "signature")
}
