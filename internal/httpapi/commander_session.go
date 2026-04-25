package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// jtiRevoker is the subset of *auth.CommanderJWTValidator that
// revokeAllSessions calls. Defining it as an interface in this file lets
// tests substitute a fake that fails individual JTIs without spinning up a
// real Redis-backed validator. Production wires s.commanderJWTValidator,
// which already satisfies this signature via its RevokeJTI method.
type jtiRevoker interface {
	RevokeJTI(ctx context.Context, jti string, expiry time.Time) error
}

// redisJTIClient is the subset of *redis.Client that revokeAllSessionsImpl
// uses. The production server passes its concrete *redis.Client; tests use
// a miniredis-backed *redis.Client too — the seam exists so a future
// in-memory test backend (without miniredis) could plug in here.
type redisJTIClient interface {
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// revokeAllSessions revokes every active JWT for fid by enumerating the
// per-FID jti tracking set commander:jtis:{fid}, adding each member to
// the revoked-jti set with a 24h TTL (longer than any active JWT's
// lifetime, so the entry naturally reaps after every active token has
// expired), then deleting the per-FID set.
//
// Idempotent: empty set is a no-op. Best-effort on individual revocations
// — a single failure is logged but does not abort the loop. The caller
// receives the FIRST encountered error (or nil), so they know revocation
// was incomplete; admin UI can surface this as "session revocation
// partially failed; some live tokens may persist until natural expiry."
//
// Called by: Deny, Unlink, Revoke, Link (Task 8). NOT called by Approve,
// Grant, or successful login — those are scope-additions and the user
// can re-login or wait for natural JWT refresh.
//
// nil redis or nil validator are treated as no-op (production safety:
// deployments without commander auth wired through Redis won't panic
// when an admin clicks Revoke).
func (s *Server) revokeAllSessions(ctx context.Context, fid string) error {
	if s.redisClient == nil || s.commanderJWTValidator == nil {
		return nil
	}
	return revokeAllSessionsImpl(ctx, fid, s.redisClient, s.commanderJWTValidator)
}

// revokeAllSessionsImpl is the testable core: it accepts any jtiRevoker and
// any redis client interface that supports SMembers / Del. We use the
// concrete *redis.Client type for the redis dependency because miniredis
// already provides one for tests, so the seam isn't needed there — the
// only seam tests exercise is the revoker.
func revokeAllSessionsImpl(ctx context.Context, fid string, rdb redisJTIClient, revoker jtiRevoker) error {
	key := "commander:jtis:" + fid
	jtis, err := rdb.SMembers(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("commander_revoke_all_sessions: smembers %s: %w", key, err)
	}
	expiry := time.Now().Add(24 * time.Hour)
	var firstErr error
	for _, jti := range jtis {
		if err := revoker.RevokeJTI(ctx, jti, expiry); err != nil {
			slog.Warn("commander_revoke_jti_failed", "fid", fid, "jti", jti, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := rdb.Del(ctx, key).Err(); err != nil {
		slog.Warn("commander_revoke_perfid_set_del_failed", "fid", fid, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
