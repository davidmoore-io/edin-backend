package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// trustEntry is a single (issuer, audience, JWKS) tuple the validator will
// accept. The validator tries each entry in order and returns the first that
// produces a valid token.
type trustEntry struct {
	issuer   string
	audience string
	jwks     keyfunc.Keyfunc
	label    string // for logging
}

// JWTValidator validates JWTs against one or more Authentik providers.
// Multiple trust entries are needed because the same control-API serves
// browser-session tokens (kaine-portal provider) and bot M2M tokens
// (edin-bot provider) — each provider has its own issuer, audience and
// (potentially) JWKS endpoint.
type JWTValidator struct {
	trusts []trustEntry
	logger *observability.Logger
}

// JWTClaims represents the expected claims in a Kaine portal or bot JWT.
type JWTClaims struct {
	jwt.RegisteredClaims
	Groups []string `json:"groups"`
	Email  string   `json:"email"`
	Name   string   `json:"name"`
}

// NewJWTValidator creates a validator that accepts the kaine-portal provider
// described by cfg, and (if cfg.BotIssuer is set) also accepts a second bot
// provider for M2M tokens.
func NewJWTValidator(cfg config.KaineAuthConfig, logger *observability.Logger) (*JWTValidator, error) {
	if !cfg.Enabled {
		return nil, errors.New("kaine auth is disabled")
	}
	if cfg.JWKSURL == "" {
		return nil, errors.New("JWKS URL is required")
	}

	v := &JWTValidator{logger: logger}

	if err := v.addTrust(cfg.JWKSURL, cfg.Issuer, cfg.Audience, "kaine-portal"); err != nil {
		return nil, err
	}

	if cfg.BotIssuer != "" || cfg.BotAudience != "" || cfg.BotJWKSURL != "" {
		if cfg.BotIssuer == "" || cfg.BotAudience == "" || cfg.BotJWKSURL == "" {
			return nil, errors.New("bot auth requires all of BOT_AUTH_ISSUER, BOT_AUTH_AUDIENCE, BOT_AUTH_JWKS_URL")
		}
		if err := v.addTrust(cfg.BotJWKSURL, cfg.BotIssuer, cfg.BotAudience, "edin-bot"); err != nil {
			return nil, err
		}
	}

	logger.Info(fmt.Sprintf("JWT validator initialized with %d trust(s)", len(v.trusts)))
	return v, nil
}

func (v *JWTValidator) addTrust(jwksURL, issuer, audience, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return fmt.Errorf("create JWKS keyfunc for %s: %w", label, err)
	}
	v.trusts = append(v.trusts, trustEntry{
		issuer: issuer, audience: audience, jwks: jwks, label: label,
	})
	v.logger.Info(fmt.Sprintf("JWT trust registered label=%s jwks=%s issuer=%s audience=%s",
		label, jwksURL, issuer, audience))
	return nil
}

// ValidateToken tries each trust entry in turn. If one accepts the token, the
// extracted user is returned. If all reject it, the error message lists each
// failure for diagnostics — every trust gets a chance because there is no way
// to know which provider issued an opaque bearer string up-front without
// parsing it (and parsing without verifying defeats the purpose).
func (v *JWTValidator) ValidateToken(tokenString string) (*KaineUser, error) {
	if len(v.trusts) == 0 {
		return nil, errors.New("no JWT trusts configured")
	}

	var failures []string
	for _, t := range v.trusts {
		claims := &JWTClaims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, t.jwks.Keyfunc,
			jwt.WithIssuer(t.issuer),
			jwt.WithAudience(t.audience),
			jwt.WithExpirationRequired(),
		)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", t.label, err))
			continue
		}
		if !token.Valid {
			failures = append(failures, fmt.Sprintf("%s: token marked invalid", t.label))
			continue
		}
		if claims.Subject == "" {
			failures = append(failures, fmt.Sprintf("%s: missing sub claim", t.label))
			continue
		}
		return &KaineUser{
			Sub:    claims.Subject,
			Groups: claims.Groups,
			Email:  claims.Email,
			Name:   claims.Name,
		}, nil
	}
	return nil, fmt.Errorf("token rejected by all trusts: %s", strings.Join(failures, "; "))
}

// Close releases resources held by the validator.
func (v *JWTValidator) Close() {
	// keyfunc v3 doesn't require explicit cleanup for the default ctx implementation.
}
