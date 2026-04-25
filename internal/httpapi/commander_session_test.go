package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/observability"
)

// TestRevokeAllSessions_RevokesEveryJTIInSet seeds two jtis under
// commander:jtis:{fid}, calls revokeAllSessions, and asserts that both
// jtis are revoked (present in the revoked-jti set) AND the per-FID set
// is deleted.
func TestRevokeAllSessions_RevokesEveryJTIInSet(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	_, validator := newCommanderTestIssuerValidator(t, rdb)

	srv := &Server{
		logger:                observability.NewLogger("test"),
		redisClient:           rdb,
		commanderJWTValidator: validator,
	}

	fid := "F2504"
	ctx := context.Background()

	_, err := rdb.SAdd(ctx, "commander:jtis:"+fid, "jti-1", "jti-2").Result()
	require.NoError(t, err)

	require.NoError(t, srv.revokeAllSessions(ctx, fid))

	// Both jtis should be in the revoked set.
	for _, jti := range []string{"jti-1", "jti-2"} {
		isMember, err := rdb.SIsMember(ctx, "edin:revoked_jtis", jti).Result()
		require.NoError(t, err)
		assert.True(t, isMember, "expected jti %s to be in revoked set", jti)
	}
	// Per-FID set should be deleted.
	exists, err := rdb.Exists(ctx, "commander:jtis:"+fid).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "expected per-FID set to be deleted")
}

// TestRevokeAllSessions_EmptySet_NoOp verifies that calling
// revokeAllSessions for a FID with no entries does not error and does
// not panic.
func TestRevokeAllSessions_EmptySet_NoOp(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	_, validator := newCommanderTestIssuerValidator(t, rdb)

	srv := &Server{
		logger:                observability.NewLogger("test"),
		redisClient:           rdb,
		commanderJWTValidator: validator,
	}

	require.NoError(t, srv.revokeAllSessions(context.Background(), "F-nobody"))
}

// fakeJTIRevoker fails RevokeJTI for one configured jti and records every
// invocation. Used by the partial-failure test.
type fakeJTIRevoker struct {
	mu      sync.Mutex
	failJTI string
	calls   []string
}

func (f *fakeJTIRevoker) RevokeJTI(_ context.Context, jti string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, jti)
	if jti == f.failJTI {
		return errFakeRevoke
	}
	return nil
}

// TestRevokeAllSessions_PartialFailure_ReturnsFirstError_ContinuesLoop
// verifies that when one jti's revocation fails, the helper still
// processes the other jti AND returns an error so the caller knows
// revocation was incomplete. Uses the jtiRevoker seam to inject a
// targeted failure — miniredis-backed validator can't fail individual
// SAdd calls selectively.
func TestRevokeAllSessions_PartialFailure_ReturnsFirstError_ContinuesLoop(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)

	fid := "F2504"
	ctx := context.Background()

	_, err := rdb.SAdd(ctx, "commander:jtis:"+fid, "jti-fail", "jti-ok").Result()
	require.NoError(t, err)

	revoker := &fakeJTIRevoker{failJTI: "jti-fail"}

	err = revokeAllSessionsImpl(ctx, fid, rdb, revoker)
	require.Error(t, err, "expected first error to surface")
	assert.True(t, errors.Is(err, errFakeRevoke), "expected wrapped fake revoke error, got: %v", err)

	// Both JTIs were attempted (loop continued past the failing one).
	assert.ElementsMatch(t, []string{"jti-fail", "jti-ok"}, revoker.calls,
		"expected loop to continue past failure and call RevokeJTI for both jtis")

	// Per-FID set is still deleted as a best-effort cleanup.
	exists, err := rdb.Exists(ctx, "commander:jtis:"+fid).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "expected per-FID set to be deleted even after partial failure")
}

// TestRevokeAllSessions_NilDeps_NoOp verifies that a server with nil
// redis or nil validator returns nil without panicking — production
// safety for deployments that haven't wired commander auth.
func TestRevokeAllSessions_NilDeps_NoOp(t *testing.T) {
	t.Run("nil redis", func(t *testing.T) {
		srv := &Server{
			logger:                observability.NewLogger("test"),
			redisClient:           nil,
			commanderJWTValidator: nil,
		}
		require.NoError(t, srv.revokeAllSessions(context.Background(), "F2504"))
	})
	t.Run("nil validator", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		srv := &Server{
			logger:                observability.NewLogger("test"),
			redisClient:           rdb,
			commanderJWTValidator: nil,
		}
		require.NoError(t, srv.revokeAllSessions(context.Background(), "F2504"))
	})
}

var errFakeRevoke = errors.New("fake revoke failure")
