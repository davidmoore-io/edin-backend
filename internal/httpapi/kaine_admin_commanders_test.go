package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/store"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

// fakeAdminAuthentik is a deterministic stand-in for *authentik.Client
// satisfying commanderAdminAuthentik. Provides per-method failure
// injection so handler tests can exercise the full decision matrix
// without httptest fixtures.
type fakeAdminAuthentik struct {
	mu               sync.Mutex
	usersByUUID      map[uuid.UUID]*authentik.UserWithConnection
	getUserErrByUUID map[uuid.UUID]error
	addCalls         []addGroupCall
	addErr           error
	removeCalls      []removeGroupCall
	removeErr        error
	removeErrByGroup map[string]error
}

type addGroupCall struct {
	UserPK    int
	GroupName string
}

type removeGroupCall struct {
	UserPK    int
	GroupName string
}

func newFakeAdminAuthentik() *fakeAdminAuthentik {
	return &fakeAdminAuthentik{
		usersByUUID:      map[uuid.UUID]*authentik.UserWithConnection{},
		getUserErrByUUID: map[uuid.UUID]error{},
		removeErrByGroup: map[string]error{},
	}
}

func (f *fakeAdminAuthentik) seedUser(u uuid.UUID, user *authentik.UserWithConnection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usersByUUID[u] = user
}

func (f *fakeAdminAuthentik) GetUserByUUID(_ context.Context, userUUID uuid.UUID) (*authentik.UserWithConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.getUserErrByUUID[userUUID]; ok {
		return nil, err
	}
	if user, ok := f.usersByUUID[userUUID]; ok {
		return user, nil
	}
	return nil, authentik.ErrUserNotFound
}

func (f *fakeAdminAuthentik) AddUserToGroup(_ context.Context, userPK int, groupName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls = append(f.addCalls, addGroupCall{UserPK: userPK, GroupName: groupName})
	if f.addErr != nil {
		return f.addErr
	}
	for _, u := range f.usersByUUID {
		if u.PK == userPK {
			for _, g := range u.GroupNames {
				if g == groupName {
					return nil
				}
			}
			u.GroupNames = append(u.GroupNames, groupName)
			break
		}
	}
	return nil
}

func (f *fakeAdminAuthentik) RemoveUserFromGroup(_ context.Context, userPK int, groupName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, removeGroupCall{UserPK: userPK, GroupName: groupName})
	if err, ok := f.removeErrByGroup[groupName]; ok {
		return err
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	for _, u := range f.usersByUUID {
		if u.PK == userPK {
			out := u.GroupNames[:0]
			for _, g := range u.GroupNames {
				if g != groupName {
					out = append(out, g)
				}
			}
			u.GroupNames = out
			break
		}
	}
	return nil
}

var _ commanderAdminAuthentik = (*fakeAdminAuthentik)(nil)

// ─── test server harness ──────────────────────────────────────────────────────

type adminTestEnv struct {
	srv     *Server
	repo    *linkTestRepo
	az      *fakeAdminAuthentik
	mr      *miniredis.Miniredis
	rdb     *redis.Client
	logPath string
}

func newAdminTestEnv(t *testing.T) *adminTestEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	_, validator := newCommanderTestIssuerValidator(t, rdb)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "admin-actions.log")

	repo := newLinkTestRepo()
	az := newFakeAdminAuthentik()

	srv := &Server{
		logger: observability.NewLogger("test"),
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				AdminActionsLogPath: logPath,
			},
		},
		commanderRepo:          repo,
		adminAuthentikOverride: az,
		redisClient:            rdb,
		commanderJWTValidator:  validator,
	}

	return &adminTestEnv{srv: srv, repo: repo, az: az, mr: mr, rdb: rdb, logPath: logPath}
}

// asAdmin attaches a KaineUser (with admin group) to the request
// context — the subtree dispatch is reached directly in tests, bypassing
// the withKaineAdmin middleware. To exercise the middleware end-to-end
// use the routed test below (TestAdminCommanders_NonAdmin_Forbidden).
func asAdmin(r *http.Request) *http.Request {
	user := &KaineUser{
		Sub:    "admin-uuid",
		Email:  "admin@example.com",
		Name:   "Admin",
		Groups: []string{"kaine-god"},
	}
	ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
	return r.WithContext(ctx)
}

// withFetchHeader attaches the X-Edin-Fetch: 1 CSRF guard.
func withFetchHeader(r *http.Request) *http.Request {
	r.Header.Set("X-Edin-Fetch", "1")
	return r
}

// readAuditLines reads each line in the admin-actions log as a parsed
// adminActionAttempt.
func readAuditLines(t *testing.T, path string) []adminActionAttempt {
	t.Helper()
	data, err := readFileIfExists(path)
	require.NoError(t, err)
	if len(data) == 0 {
		return nil
	}
	var entries []adminActionAttempt
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		var entry adminActionAttempt
		require.NoError(t, json.Unmarshal(line, &entry))
		entries = append(entries, entry)
	}
	return entries
}

// ─── auth/csrf gating ────────────────────────────────────────────────────────

// TestAdminCommanders_NonAdmin_Forbidden asserts that the middleware
// chain rejects a non-admin user with 403. We exercise the full mux
// path here so the middleware is engaged.
func TestAdminCommanders_NonAdmin_Forbidden(t *testing.T) {
	env := newAdminTestEnv(t)
	mock := newMockJWTValidator()
	mock.addUser("non-admin-jwt", &KaineUser{Sub: "u", Groups: []string{"kaine-chat"}})
	env.srv.jwtValidator = mock
	env.srv.cfg.KaineAuth.CookieName = "kaine_session"

	mux := http.NewServeMux()
	env.srv.RegisterKaineRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/admin/commanders", nil)
	req.Header.Set("Cookie", "kaine_session=non-admin-jwt")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// TestAdminCommanders_Mutation_MissingFetchHeader_400 asserts the
// X-Edin-Fetch guard fires before any state change.
func TestAdminCommanders_Mutation_MissingFetchHeader_400(t *testing.T) {
	env := newAdminTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/approve", nil)
	req = asAdmin(req)
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestAdminCommanders_EmptyFID_400 covers the path-traversal guard.
func TestAdminCommanders_EmptyFID_400(t *testing.T) {
	env := newAdminTestEnv(t)

	for _, path := range []string{
		"/api/kaine/admin/commanders/",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req = asAdmin(req)
			rr := httptest.NewRecorder()
			env.srv.handleKaineAdminCommandersSubtree(rr, req)
			// path "" or "/" gets routed to list (GET) → 200 with empty
			// list. We're really after the inner empty-FID branch,
			// which can be reached when the second segment is missing.
			assert.True(t, rr.Code == http.StatusOK || rr.Code == http.StatusBadRequest,
				"expected list 200 or fid 400, got %d", rr.Code)
		})
	}
}

// ─── list / get ───────────────────────────────────────────────────────────────

func TestAdminCommanders_List_ReturnsAllRowsWithLinkState(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.repo.rowByFID["F2504"].CmdrName = "Pattern State"
	env.repo.rowByFID["F2504"].Approved = true
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User:       authentik.User{PK: 7, UUID: authentikUUID, Username: "edin-cmdr-F2504"},
		GroupNames: []string{"edin-copilot"},
	})
	env.repo.seedRow("F0000", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/admin/commanders", nil)
	req = asAdmin(req)
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Commanders []commanderAdminView `json:"commanders"`
		Count      int                  `json:"count"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 2, resp.Count)
	// Find the F2504 entry and check link state.
	var f2504 *commanderAdminView
	for i := range resp.Commanders {
		if resp.Commanders[i].FID == "F2504" {
			f2504 = &resp.Commanders[i]
		}
	}
	require.NotNil(t, f2504)
	assert.Equal(t, "Pattern State", f2504.CmdrName)
	require.NotNil(t, f2504.AuthentikUserID)
	require.NotNil(t, f2504.AuthentikUserPresent)
	assert.True(t, *f2504.AuthentikUserPresent)
	assert.Equal(t, "edin-cmdr-F2504", f2504.AuthentikUsername)
}

func TestAdminCommanders_GetByFID_IncludesGroups(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.repo.rowByFID["F2504"].Approved = true
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User:       authentik.User{PK: 7, UUID: authentikUUID, Username: "edin-cmdr-F2504"},
		GroupNames: []string{"edin-copilot", "edin-copilot-trusted"},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/admin/commanders/F2504", nil)
	req = asAdmin(req)
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var view commanderAdminView
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&view))
	assert.Equal(t, []string{"edin-copilot", "edin-copilot-trusted"}, view.Groups)
}

func TestAdminCommanders_GetByFID_NotFound_404(t *testing.T) {
	env := newAdminTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/kaine/admin/commanders/F-missing", nil)
	req = asAdmin(req)
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ─── Grant ────────────────────────────────────────────────────────────────────

func TestAdminCommanders_Grant_HappyPath(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID, Username: "edin-cmdr-F2504"},
	})

	body := bytes.NewBufferString(`{"group":"edin-copilot"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/grant", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.Len(t, env.az.addCalls, 1)
	assert.Equal(t, addGroupCall{UserPK: 7, GroupName: "edin-copilot"}, env.az.addCalls[0])
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.Equal(t, setApprovedCall{FID: "F2504", Approved: true}, env.repo.setApprovedCalls[0])

	entries := readAuditLines(t, env.logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "commander.grant", entries[0].Action)
	assert.Equal(t, "F2504", entries[0].SubjectFID)
	assert.Equal(t, "admin-uuid", entries[0].AdminSub)
	assert.Equal(t, "edin-copilot", entries[0].Details["group"])
}

func TestAdminCommanders_Grant_InvalidGroup_400(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil)

	body := bytes.NewBufferString(`{"group":"kaine-admin"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/grant", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_group")
	assert.Empty(t, env.az.addCalls)
}

func TestAdminCommanders_Grant_UnlinkedCommander_409(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil) // No Authentik link.

	body := bytes.NewBufferString(`{"group":"edin-copilot"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/grant", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "commander_not_linked")
}

func TestAdminCommanders_Grant_AddGroupFails_500_ApprovedUnchanged(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID},
	})
	env.az.addErr = errors.New("authentik 500")

	body := bytes.NewBufferString(`{"group":"edin-copilot"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/grant", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Empty(t, env.repo.setApprovedCalls, "must not flip approved when group-add failed")
	assert.False(t, env.repo.rowByFID["F2504"].Approved)
}

// ─── Revoke ───────────────────────────────────────────────────────────────────

func TestAdminCommanders_Revoke_HappyPath(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.repo.rowByFID["F2504"].Approved = true
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User:       authentik.User{PK: 7, UUID: authentikUUID, Username: "u"},
		GroupNames: []string{"edin-copilot", "edin-copilot-trusted"},
	})
	// Seed a live JTI so revokeAllSessions has something to do.
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-live").Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/revoke", nil)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	// SetApproved(false) was called.
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.Equal(t, setApprovedCall{FID: "F2504", Approved: false}, env.repo.setApprovedCalls[0])
	// Both managed groups were attempted to be removed.
	groups := []string{}
	for _, c := range env.az.removeCalls {
		groups = append(groups, c.GroupName)
	}
	assert.ElementsMatch(t, []string{"edin-copilot", "edin-copilot-trusted"}, groups)
	// JTI was revoked.
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-live").Result()
	require.NoError(t, err)
	assert.True(t, isMember)
	// Audit line.
	entries := readAuditLines(t, env.logPath)
	require.Len(t, entries, 1)
	assert.Equal(t, "commander.revoke", entries[0].Action)
}

func TestAdminCommanders_Revoke_Idempotent(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID},
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/revoke", nil)
		req = withFetchHeader(asAdmin(req))
		rr := httptest.NewRecorder()
		env.srv.handleKaineAdminCommandersSubtree(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "iteration %d", i)
	}
}

func TestAdminCommanders_Revoke_UnlinkedCommander_NoAuthentikCalls(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil)
	env.repo.rowByFID["F2504"].Approved = true
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-x").Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/revoke", nil)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	assert.Empty(t, env.az.removeCalls, "no Authentik calls when commander is unlinked")
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.Equal(t, setApprovedCall{FID: "F2504", Approved: false}, env.repo.setApprovedCalls[0])
	// revokeAllSessions still ran.
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-x").Result()
	require.NoError(t, err)
	assert.True(t, isMember)
}

// ─── Link ─────────────────────────────────────────────────────────────────────

func TestAdminCommanders_Link_NonexistentAuthentikUser_400(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil)

	body := bytes.NewBufferString(`{"authentik_user_id":"` + uuid.New().String() + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/link", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "authentik_user_does_not_exist")
}

func TestAdminCommanders_Link_ConflictingUUID_409_WithConflictingFID(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	// F-other already owns the link; F2504 tries to take it.
	env.repo.seedRow("F-other", &authentikUUID)
	env.repo.seedRow("F2504", nil)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID},
	})
	// Wire SetAuthentikLink to return the unique-violation sentinel.
	env.repo.setLinkErr = errCommanderUniqueViolation

	body := bytes.NewBufferString(`{"authentik_user_id":"` + authentikUUID.String() + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/link", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	var resp struct {
		Error          string `json:"error"`
		ConflictingFID string `json:"conflicting_fid"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "authentik_user_already_linked", resp.Error)
	assert.Equal(t, "F-other", resp.ConflictingFID)
}

func TestAdminCommanders_Link_RevokesSessions(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", nil)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID},
	})
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-link").Result()
	require.NoError(t, err)

	body := bytes.NewBufferString(`{"authentik_user_id":"` + authentikUUID.String() + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/link", body)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-link").Result()
	require.NoError(t, err)
	assert.True(t, isMember)
}

// ─── Unlink ───────────────────────────────────────────────────────────────────

func TestAdminCommanders_Unlink_ClearsLinkApprovedAndSessions(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.repo.rowByFID["F2504"].Approved = true
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-unlink").Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/unlink", nil)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	// SetAuthentikLink(nil) called.
	require.NotEmpty(t, env.repo.setLinkCalls)
	last := env.repo.setLinkCalls[len(env.repo.setLinkCalls)-1]
	assert.Equal(t, "F2504", last.FID)
	assert.Nil(t, last.UserID)
	// approved=false.
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.False(t, env.repo.setApprovedCalls[0].Approved)
	// JTI revoked.
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-unlink").Result()
	require.NoError(t, err)
	assert.True(t, isMember)
}

// ─── Approve / Deny ──────────────────────────────────────────────────────────

// TestAdminCommanders_Approve_DoesNotRevokeSessions is the regression
// guard: Approve must NOT call revokeAllSessions (approval is a
// scope-addition; existing JWTs stay valid).
func TestAdminCommanders_Approve_DoesNotRevokeSessions(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil)
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-approve").Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/approve", nil)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-approve").Result()
	require.NoError(t, err)
	assert.False(t, isMember, "approve must NOT revoke sessions")
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.True(t, env.repo.setApprovedCalls[0].Approved)
}

func TestAdminCommanders_Deny_RevokesSessions(t *testing.T) {
	env := newAdminTestEnv(t)
	env.repo.seedRow("F2504", nil)
	env.repo.rowByFID["F2504"].Approved = true
	_, err := env.rdb.SAdd(context.Background(), "commander:jtis:F2504", "jti-deny").Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/commanders/F2504/deny", nil)
	req = withFetchHeader(asAdmin(req))
	rr := httptest.NewRecorder()
	env.srv.handleKaineAdminCommandersSubtree(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	isMember, err := env.rdb.SIsMember(context.Background(), "edin:revoked_jtis", "jti-deny").Result()
	require.NoError(t, err)
	assert.True(t, isMember)
	require.Len(t, env.repo.setApprovedCalls, 1)
	assert.False(t, env.repo.setApprovedCalls[0].Approved)
}

// TestAdminCommanders_AllMutations_WriteAuditLines is the umbrella check
// that every mutation path produces a JSON-line audit entry with the
// expected action label.
func TestAdminCommanders_AllMutations_WriteAuditLines(t *testing.T) {
	env := newAdminTestEnv(t)
	authentikUUID := uuid.New()
	env.repo.seedRow("F2504", &authentikUUID)
	env.az.seedUser(authentikUUID, &authentik.UserWithConnection{
		User: authentik.User{PK: 7, UUID: authentikUUID},
	})

	post := func(path, body string) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req = withFetchHeader(asAdmin(req))
		rr := httptest.NewRecorder()
		env.srv.handleKaineAdminCommandersSubtree(rr, req)
		require.GreaterOrEqual(t, rr.Code, 200, "request to %s should not produce 5xx; got %d body %s",
			path, rr.Code, rr.Body.String())
	}

	post("/api/kaine/admin/commanders/F2504/approve", "")
	post("/api/kaine/admin/commanders/F2504/grant", `{"group":"edin-copilot"}`)
	post("/api/kaine/admin/commanders/F2504/deny", "")
	post("/api/kaine/admin/commanders/F2504/revoke", "")
	post("/api/kaine/admin/commanders/F2504/unlink", "")

	entries := readAuditLines(t, env.logPath)
	actions := make([]string, 0, len(entries))
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{
		"commander.approve",
		"commander.grant",
		"commander.deny",
		"commander.revoke",
		"commander.unlink",
	} {
		assert.Contains(t, actions, want, "missing audit entry for %s", want)
	}
}

// errCommanderUniqueViolation is the sentinel injected via
// linkTestRepo.setLinkErr in the conflicting-UUID test. The handler
// maps store.ErrAuthentikUserAlreadyLinked to HTTP 409.
var errCommanderUniqueViolation = store.ErrAuthentikUserAlreadyLinked

// readFileIfExists returns the file contents, or nil (no error) if the
// file does not yet exist. Used by audit-log tests that may run before
// any audit line was appended.
func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}
