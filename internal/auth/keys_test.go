package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// helper: generate a PEM-encoded RSA private key of the given bit size (PKCS#8).
func generatePKCS8PrivateKeyPEM(t *testing.T, bits int) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// helper: generate a PEM-encoded RSA private key of the given bit size (PKCS#1).
func generatePKCS1PrivateKeyPEM(t *testing.T, bits int) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

// helper: generate a PEM-encoded PKIX RSA public key.
func generatePublicKeyPEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// helper: generate a PEM-encoded PKIX EC public key.
func generateECPublicKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func TestLoadPrivateKey_ValidPEM(t *testing.T) {
	// PKCS#8 (preferred)
	pemData := generatePKCS8PrivateKeyPEM(t, 2048)
	key, err := auth.LoadPrivateKey(pemData)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, 2048, key.N.BitLen())

	// PKCS#1 (backwards compat)
	pemData1 := generatePKCS1PrivateKeyPEM(t, 2048)
	key1, err := auth.LoadPrivateKey(pemData1)
	require.NoError(t, err)
	require.NotNil(t, key1)
	require.Equal(t, 2048, key1.N.BitLen())
}

func TestLoadPrivateKey_InvalidPEM_ReturnsError(t *testing.T) {
	_, err := auth.LoadPrivateKey([]byte("this is not a PEM block"))
	require.ErrorIs(t, err, auth.ErrInvalidPEM)
}

func TestLoadPrivateKey_1024BitKey_ReturnsError(t *testing.T) {
	pemData := generatePKCS8PrivateKeyPEM(t, 1024)
	_, err := auth.LoadPrivateKey(pemData)
	require.ErrorIs(t, err, auth.ErrKeyTooSmall)
}

func TestLoadPublicKey_ValidPEM(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemData := generatePublicKeyPEM(t, rsaKey)

	pub, err := auth.LoadPublicKey(pemData)
	require.NoError(t, err)
	require.NotNil(t, pub)
	require.Equal(t, 2048, pub.N.BitLen())
}

func TestLoadPublicKey_NonRSAKey_ReturnsError(t *testing.T) {
	pemData := generateECPublicKeyPEM(t)
	_, err := auth.LoadPublicKey(pemData)
	require.ErrorIs(t, err, auth.ErrNotRSAKey)
}

func TestRoundtrip_SignAndVerify(t *testing.T) {
	// Generate a fresh 2048-bit key and round-trip through PEM.
	rawKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Encode private key as PKCS#8 PEM.
	privDER, err := x509.MarshalPKCS8PrivateKey(rawKey)
	require.NoError(t, err)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	// Encode public key as PKIX PEM.
	pubDER, err := x509.MarshalPKIXPublicKey(&rawKey.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	// Load via our library.
	privKey, err := auth.LoadPrivateKey(privPEM)
	require.NoError(t, err)

	pubKey, err := auth.LoadPublicKey(pubPEM)
	require.NoError(t, err)

	// Sign a JWT.
	claims := jwt.RegisteredClaims{
		Subject:   "cmdr-test-123",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privKey)
	require.NoError(t, err)
	require.NotEmpty(t, tokenString)

	// Verify the JWT with the public key.
	parsed, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{}, func(t *jwt.Token) (interface{}, error) {
		return pubKey, nil
	})
	require.NoError(t, err)
	require.True(t, parsed.Valid)

	// Verify sub claim survives round-trip.
	parsedClaims, ok := parsed.Claims.(*jwt.RegisteredClaims)
	require.True(t, ok)
	require.Equal(t, "cmdr-test-123", parsedClaims.Subject)

	// Tampered token must return an error.
	parts := strings.SplitN(tokenString, ".", 3)
	require.Len(t, parts, 3)
	tampered := parts[0] + "." + parts[1] + ".invalidsignature"
	_, err = jwt.ParseWithClaims(tampered, &jwt.RegisteredClaims{}, func(t *jwt.Token) (interface{}, error) {
		return pubKey, nil
	})
	require.Error(t, err)
}
