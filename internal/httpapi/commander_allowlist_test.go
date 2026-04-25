package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/store"
)

// ─── fidAllowed helper retained ───────────────────────────────────────────────

func TestFIDAllowed_EmptyAllowlist_AllowsEveryone(t *testing.T) {
	// An empty allowlist is the "disabled" state. Any caller proceeds.
	assert.True(t, fidAllowed(nil, "F2504"))
	assert.True(t, fidAllowed([]string{}, "F9999"))
}

func TestFIDAllowed_Membership(t *testing.T) {
	list := []string{"F2504", "F123"}
	assert.True(t, fidAllowed(list, "F2504"))
	assert.True(t, fidAllowed(list, "F123"))
	assert.False(t, fidAllowed(list, "F9999"))
}

func TestFIDAllowed_CaseSensitive(t *testing.T) {
	// FIDs from Frontier are always uppercase "F" + digits. Treat case
	// mismatches as "not on list" so a deployment that accidentally writes
	// "f2504" in config doesn't quietly grant access to both forms.
	list := []string{"F2504"}
	assert.False(t, fidAllowed(list, "f2504"))
}

// ─── resolveCommanderAccess decision-matrix tests ─────────────────────────────

// TestResolveCommanderAccess_LinkedApproved_UsesAuthentikGroups covers the
// primary post-Task-5 path: a linked + approved commander whose Authentik
// user has a recognised group → scopes derived from authz.ScopesForGroups.
func TestResolveCommanderAccess_LinkedApproved_UsesAuthentikGroups(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)
	row := repo.rowByFID[fid]
	row.Approved = true

	groups := &fakeAuthentikUserGroups{
		userByUUID: map[uuid.UUID]*authentik.UserWithConnection{
			authentikUUID: {GroupNames: []string{"edin-copilot"}},
		},
	}

	srv := newAccessTestServer(t, nil, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.True(t, dec.Allowed, "linked+approved with mapped group must be allowed")
	assert.Equal(t, "authentik_groups", dec.Reason)
	assert.Nil(t, dec.Denial)
	assert.Equal(t, []authz.Scope{
		authz.ScopeCommanderData,
		authz.ScopeCopilotChat,
		authz.ScopeGalaxyRead,
	}, dec.Scopes, "scopes must match ScopesForGroups([edin-copilot])")
}

// TestResolveCommanderAccess_LinkedApproved_NoGroupsMapped_Denied covers the
// case where Authentik returns groups but none of them map to a scope set.
// Plan: deny with reason=no_scopes_granted. Must NOT fall through to the
// allowlist branch.
func TestResolveCommanderAccess_LinkedApproved_NoGroupsMapped_Denied(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)
	row := repo.rowByFID[fid]
	row.Approved = true

	groups := &fakeAuthentikUserGroups{
		userByUUID: map[uuid.UUID]*authentik.UserWithConnection{
			authentikUUID: {GroupNames: []string{"totally-unknown-group"}},
		},
	}

	// Allowlist contains the FID; the linked+approved branch must NOT fall
	// through to allowlist_fallback. This is a critical safety property.
	srv := newAccessTestServer(t, []string{fid}, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "no_scopes_granted", dec.Reason)
	assert.Nil(t, dec.Scopes)
	require.NotNil(t, dec.Denial)
	assert.Equal(t, "no_scopes_granted", dec.Denial.Reason)
	assert.Equal(t, fid, dec.Denial.FID)
	assert.Equal(t, loginFlowWeb, dec.Denial.Flow)
}

// TestResolveCommanderAccess_LinkedApproved_AuthentikUserDeleted_DeniesWithMissingReason
// covers the "shadow user wiped from Authentik" failure mode.
func TestResolveCommanderAccess_LinkedApproved_AuthentikUserDeleted_DeniesWithMissingReason(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)
	row := repo.rowByFID[fid]
	row.Approved = true

	groups := &fakeAuthentikUserGroups{
		errByUUID: map[uuid.UUID]error{authentikUUID: authentik.ErrUserNotFound},
	}

	srv := newAccessTestServer(t, nil, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "authentik_user_missing", dec.Reason)
	require.NotNil(t, dec.Denial)
	assert.Equal(t, "authentik_user_missing", dec.Denial.Reason)
}

// TestResolveCommanderAccess_LinkedApproved_AuthentikTransientError_DeniesClosed
// covers transient Authentik failures (timeout, network, 5xx) — they must
// deny-closed with reason=authentik_unreachable.
func TestResolveCommanderAccess_LinkedApproved_AuthentikTransientError_DeniesClosed(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)
	row := repo.rowByFID[fid]
	row.Approved = true

	groups := &fakeAuthentikUserGroups{
		errByUUID: map[uuid.UUID]error{authentikUUID: errors.New("authentik 502 bad gateway")},
	}

	srv := newAccessTestServer(t, nil, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "authentik_unreachable", dec.Reason)
	require.NotNil(t, dec.Denial)
}

// TestResolveCommanderAccess_LinkedApproved_AuthentikTimeout_DeniesUnreachable
// pins the 2-second timeout: an Authentik fake that blocks past the deadline
// must surface as authentik_unreachable.
func TestResolveCommanderAccess_LinkedApproved_AuthentikTimeout_DeniesUnreachable(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)
	row := repo.rowByFID[fid]
	row.Approved = true

	// blockUntilCancelled mimics Authentik blocking past the 2s deadline; the
	// fake honours ctx so we don't actually wait 2 seconds in the test.
	groups := &fakeAuthentikUserGroups{blockUntilCancelled: true}

	srv := newAccessTestServer(t, nil, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	// Use a parent context with a short deadline so the test completes
	// quickly while still proving the inner timeout path is honoured.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(ctx, r, loginFlowWeb, fid, "Pattern State")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "authentik_unreachable", dec.Reason)
}

// TestResolveCommanderAccess_LinkedNotApproved_OnAllowlist_UsesFallback covers
// the migration window: a linked-but-not-yet-approved commander whose FID is
// on the env-var allowlist gets the default commander scope set. The
// Authentik client is NOT consulted — approved=false short-circuits.
func TestResolveCommanderAccess_LinkedNotApproved_OnAllowlist_UsesFallback(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID) // Approved defaults to false on a freshly-seeded row.

	groups := &fakeAuthentikUserGroups{} // Should NOT be consulted.

	srv := newAccessTestServer(t, []string{fid}, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = groups

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.True(t, dec.Allowed)
	assert.Equal(t, "allowlist_fallback", dec.Reason)
	assert.Equal(t, []authz.Scope{
		authz.ScopeCopilotChat,
		authz.ScopeGalaxyRead,
		authz.ScopeCommanderData,
	}, dec.Scopes, "default commander scope set")
	assert.Equal(t, 0, groups.calls, "Authentik must NOT be consulted for not-approved rows")
}

// TestResolveCommanderAccess_LinkedNotApproved_OffAllowlist_DeniesAwaiting
// covers the post-rollout shape: linked but awaiting admin approval, with
// no env-var fallback.
func TestResolveCommanderAccess_LinkedNotApproved_OffAllowlist_DeniesAwaiting(t *testing.T) {
	fid := "F2504"
	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")

	repo := newLinkTestRepo()
	repo.seedRow(fid, &authentikUUID)

	srv := newAccessTestServer(t, []string{"F-other"}, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = &fakeAuthentikUserGroups{}

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "awaiting_approval", dec.Reason)
	require.NotNil(t, dec.Denial)
	assert.Equal(t, "awaiting_approval", dec.Denial.Reason)
}

// TestResolveCommanderAccess_RowAbsent_OnAllowlist_UsesFallback covers the
// transitional path (a row should always exist post-Task-5, but the legacy
// allowlist branch is kept until Task 12).
func TestResolveCommanderAccess_RowAbsent_OnAllowlist_UsesFallback(t *testing.T) {
	fid := "F2504"
	repo := newLinkTestRepo() // No seedRow — GetCommanderAsAdmin returns ErrCommanderNotFound.

	srv := newAccessTestServer(t, []string{fid}, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = &fakeAuthentikUserGroups{}

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Pattern State")

	assert.True(t, dec.Allowed)
	assert.Equal(t, "allowlist", dec.Reason,
		"row absent + on allowlist must surface reason=allowlist (distinct from allowlist_fallback)")
	assert.Equal(t, []authz.Scope{
		authz.ScopeCopilotChat,
		authz.ScopeGalaxyRead,
		authz.ScopeCommanderData,
	}, dec.Scopes)
}

// TestResolveCommanderAccess_RowAbsent_OffAllowlist_Denied covers the
// transitional reject path.
func TestResolveCommanderAccess_RowAbsent_OffAllowlist_Denied(t *testing.T) {
	fid := "F9999"
	repo := newLinkTestRepo()

	srv := newAccessTestServer(t, []string{"F2504"}, "")
	srv.commanderRepo = repo
	srv.authentikUserGroups = &fakeAuthentikUserGroups{}

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	dec := srv.resolveCommanderAccess(context.Background(), r, loginFlowWeb, fid, "Stranger")

	assert.False(t, dec.Allowed)
	assert.Equal(t, "not_on_allowlist", dec.Reason)
	require.NotNil(t, dec.Denial)
	assert.Equal(t, "not_on_allowlist", dec.Denial.Reason)
	assert.Equal(t, fid, dec.Denial.FID)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// newAccessTestServer produces a Server stub with the fields
// resolveCommanderAccess reads, plus a logger.
func newAccessTestServer(t *testing.T, allowlist []string, logPath string) *Server {
	t.Helper()
	return &Server{
		logger: observability.NewLogger("test"),
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				AllowedFIDs:         allowlist,
				LoginAttemptLogPath: logPath,
			},
		},
	}
}

// fakeAuthentikUserGroups is a deterministic stand-in for
// *authentik.Client (the production type) implementing only the
// authentikUserGroupResolver interface used by resolveCommanderAccess.
//
// userByUUID returns the configured user; errByUUID overrides with an
// error (e.g. authentik.ErrUserNotFound or a generic transient error).
// blockUntilCancelled exercises the 2-second timeout: the fake blocks on
// ctx.Done() and returns context.DeadlineExceeded.
type fakeAuthentikUserGroups struct {
	mu sync.Mutex

	userByUUID          map[uuid.UUID]*authentik.UserWithConnection
	errByUUID           map[uuid.UUID]error
	blockUntilCancelled bool

	calls int
}

func (f *fakeAuthentikUserGroups) GetUserByUUID(ctx context.Context, userUUID uuid.UUID) (*authentik.UserWithConnection, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.blockUntilCancelled {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err, ok := f.errByUUID[userUUID]; ok {
		return nil, err
	}
	if u, ok := f.userByUUID[userUUID]; ok {
		return u, nil
	}
	return nil, authentik.ErrUserNotFound
}

// Compile-time interface check: keep this near the fake so a future change
// to authentikUserGroupResolver surfaces here, not at every test call site.
var _ authentikUserGroupResolver = (*fakeAuthentikUserGroups)(nil)

// errCommanderNotFoundReexport guarantees the test file references the
// store sentinel — keeps the import "live" if the only other reference
// becomes indirect via linkTestRepo.
var errCommanderNotFoundReexport = store.ErrCommanderNotFound

func init() {
	// Suppress "declared but not used" on the sentinel reference above.
	_ = errCommanderNotFoundReexport
}
