package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

// TokenValidator defines the interface for JWT validation.
// This allows mocking in tests.
type TokenValidator interface {
	ValidateToken(token string) (*KaineUser, error)
	Close()
}

// JWTValidator validates Kaine portal JWTs against the Authentik JWKS endpoint.
type JWTValidator struct {
	jwks     keyfunc.Keyfunc
	issuer   string
	audience string
	logger   *observability.Logger
}

// JWTClaims represents the expected claims in a Kaine portal JWT.
type JWTClaims struct {
	jwt.RegisteredClaims
	Groups []string `json:"groups"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
}

// NewJWTValidator creates a new JWT validator that fetches keys from the JWKS endpoint.
func NewJWTValidator(cfg config.KaineAuthConfig, logger *observability.Logger) (*JWTValidator, error) {
	if !cfg.Enabled {
		return nil, errors.New("kaine auth is disabled")
	}

	if cfg.JWKSURL == "" {
		return nil, errors.New("JWKS URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create keyfunc with automatic refresh
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS keyfunc: %w", err)
	}

	logger.Info(fmt.Sprintf("JWT validator initialized jwks_url=%s issuer=%s audience=%s", cfg.JWKSURL, cfg.Issuer, cfg.Audience))

	return &JWTValidator{
		jwks:     jwks,
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		logger:   logger,
	}, nil
}

// ValidateToken validates a JWT and extracts the user information.
func (v *JWTValidator) ValidateToken(tokenString string) (*KaineUser, error) {
	claims := &JWTClaims{}

	// Parse and validate the token
	token, err := jwt.ParseWithClaims(tokenString, claims, v.jwks.Keyfunc,
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	if !token.Valid {
		return nil, errors.New("token is not valid")
	}

	// Require subject claim
	if claims.Subject == "" {
		return nil, errors.New("token missing required sub claim")
	}

	// Build KaineUser from claims
	user := &KaineUser{
		Sub:    claims.Subject,
		Groups: claims.Groups,
		Email:  claims.Email,
		Name:   claims.Name,
	}

	return user, nil
}

// Close releases resources held by the validator.
func (v *JWTValidator) Close() {
	// keyfunc v3 doesn't require explicit cleanup for the default ctx implementation
}
