package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// revokedJTISetKey is the Redis Set key used to track revoked JTIs.
const revokedJTISetKey = "edin:revoked_jtis"

// ErrTokenRevoked is returned when a JTI has been explicitly revoked (e.g. after logout).
var ErrTokenRevoked = errors.New("auth: token has been revoked")

// ErrNoFIDInContext is returned by FIDFromContext when the middleware was not applied.
var ErrNoFIDInContext = errors.New("auth: no FID in context")

// CommanderClaims are the JWT claims embedded in EDIN commander session tokens.
type CommanderClaims struct {
	FID  string `json:"fid"`  // Frontier ID e.g. "F2504"
	Name string `json:"name"` // Commander name e.g. "Pattern State"
	jwt.RegisteredClaims
}

// CommanderJWTIssuer signs RS256 JWTs for authenticated commanders.
type CommanderJWTIssuer struct {
	privateKey *rsa.PrivateKey
	issuer     string
	expiry     time.Duration
}

// NewCommanderJWTIssuer creates a new issuer.
func NewCommanderJWTIssuer(privateKey *rsa.PrivateKey, issuer string, expiry time.Duration) *CommanderJWTIssuer {
	return &CommanderJWTIssuer{
		privateKey: privateKey,
		issuer:     issuer,
		expiry:     expiry,
	}
}

// Issue signs a new JWT for the given FID and commander name.
// The jti claim is a random UUID. Returns the signed token string and the jti.
func (i *CommanderJWTIssuer) Issue(fid, name string) (tokenString string, jti string, err error) {
	jti = uuid.NewString()
	now := time.Now()

	claims := CommanderClaims{
		FID:  fid,
		Name: name,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.expiry)),
			ID:        jti,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err = token.SignedString(i.privateKey)
	if err != nil {
		return "", "", fmt.Errorf("auth: signing token: %w", err)
	}

	return tokenString, jti, nil
}

// RedisRevoker is the subset of redis.Client used for JTI revocation checks.
type RedisRevoker interface {
	SIsMember(ctx context.Context, key string, member interface{}) *redis.BoolCmd
	SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	ExpireAt(ctx context.Context, key string, tm time.Time) *redis.BoolCmd
}

// CommanderJWTValidator verifies RS256 JWTs and checks the Redis revoked-JTI set.
type CommanderJWTValidator struct {
	publicKey *rsa.PublicKey
	issuer    string
	redis     RedisRevoker
}

// NewCommanderJWTValidator creates a new validator.
func NewCommanderJWTValidator(publicKey *rsa.PublicKey, issuer string, rdb RedisRevoker) *CommanderJWTValidator {
	return &CommanderJWTValidator{
		publicKey: publicKey,
		issuer:    issuer,
		redis:     rdb,
	}
}

// Validate verifies the token, checks revocation, and returns the claims.
// Returns ErrTokenRevoked if the JTI is in the revoked set.
func (v *CommanderJWTValidator) Validate(ctx context.Context, tokenString string) (*CommanderClaims, error) {
	claims := &CommanderClaims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method: %v", t.Header["alg"])
		}
		return v.publicKey, nil
	}, jwt.WithIssuer(v.issuer), jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		return nil, fmt.Errorf("auth: parsing token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("auth: token is not valid")
	}

	// Check JTI revocation in Redis.
	jti := claims.ID
	if jti != "" {
		revoked, err := v.redis.SIsMember(ctx, revokedJTISetKey, jti).Result()
		if err != nil {
			return nil, fmt.Errorf("auth: checking revocation: %w", err)
		}
		if revoked {
			return nil, ErrTokenRevoked
		}
	}

	return claims, nil
}

// RevokeJTI adds the JTI to the Redis revoked set with TTL set to the token's expiry.
// Call this during logout.
func (v *CommanderJWTValidator) RevokeJTI(ctx context.Context, jti string, expiry time.Time) error {
	if err := v.redis.SAdd(ctx, revokedJTISetKey, jti).Err(); err != nil {
		return fmt.Errorf("auth: adding JTI to revoked set: %w", err)
	}
	if err := v.redis.ExpireAt(ctx, revokedJTISetKey, expiry).Err(); err != nil {
		return fmt.Errorf("auth: setting TTL on revoked set: %w", err)
	}
	return nil
}

// commanderClaimsKey is the unexported type used as context key for commander claims.
type commanderClaimsKey struct{}

// WithCommanderClaims stores validated claims in the context.
func WithCommanderClaims(ctx context.Context, claims *CommanderClaims) context.Context {
	return context.WithValue(ctx, commanderClaimsKey{}, claims)
}

// FIDFromContext extracts the FID from context set by withCommanderAuth middleware.
// Returns ErrNoFIDInContext if the middleware was not applied.
// Does NOT panic — returns a typed error.
func FIDFromContext(ctx context.Context) (string, error) {
	claims, ok := ctx.Value(commanderClaimsKey{}).(*CommanderClaims)
	if !ok || claims == nil {
		return "", ErrNoFIDInContext
	}
	return claims.FID, nil
}

// ClaimsFromContext extracts the full CommanderClaims from context.
// Returns ErrNoFIDInContext if the middleware was not applied.
func ClaimsFromContext(ctx context.Context) (*CommanderClaims, error) {
	claims, ok := ctx.Value(commanderClaimsKey{}).(*CommanderClaims)
	if !ok || claims == nil {
		return nil, ErrNoFIDInContext
	}
	return claims, nil
}
