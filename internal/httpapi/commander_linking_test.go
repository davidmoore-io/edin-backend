package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
)

// linkTestRepo is a minimal mock CommanderRepository tailored to the
// callback-side auto-link tests in commander_auth_test.go and
// commander_client_auth_test.go. It supports configurable behaviour for the
// three methods the link path exercises (UpsertCommander,
// GetCommanderAsAdmin, SetAuthentikLink) and panics for everything else so
// accidental dependencies fail loudly.
type linkTestRepo struct {
	mu sync.Mutex

	// Upsert
	upsertCalls []upsertCall
	upsertErr   error

	// GetCommanderAsAdmin
	rowByFID map[string]*store.CommanderRow
	getErr   error

	// defaultApproved — when true, GetCommanderAsAdmin synthesises an
	// approved + linked row for any FID not explicitly seeded. Used by
	// newPermissiveLinkTestRepo to install a "happy path" access state in
	// the post-Task-12 callback tests where the env-var allowlist is gone
	// and the default decision must be "allow" rather than the previous
	// "empty allowlist = open" semantics.
	defaultApproved bool

	// SetAuthentikLink
	setLinkCalls []setLinkCall
	setLinkErr   error

	// SetApproved (Task 8)
	setApprovedCalls []setApprovedCall
	setApprovedErr   error

	// ListAllCommanders (Task 8)
	listErr error
}

type upsertCall struct {
	FID, Name, Platform string
}

type setLinkCall struct {
	FID    string
	UserID *uuid.UUID
}

type setApprovedCall struct {
	FID      string
	Approved bool
}

func newLinkTestRepo() *linkTestRepo {
	return &linkTestRepo{rowByFID: make(map[string]*store.CommanderRow)}
}

// seedRow installs a CommanderRow that GetCommanderAsAdmin will return for
// the given fid.
func (m *linkTestRepo) seedRow(fid string, authentikUserID *uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rowByFID[fid] = &store.CommanderRow{
		ID:              uuid.New(),
		FID:             fid,
		Platform:        "frontier",
		FirstSeenAt:     time.Now(),
		LastSeenAt:      time.Now(),
		AuthentikUserID: authentikUserID,
	}
}

func (m *linkTestRepo) UpsertCommander(_ context.Context, fid, name, platform string) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertCalls = append(m.upsertCalls, upsertCall{FID: fid, Name: name, Platform: platform})
	if m.upsertErr != nil {
		return uuid.Nil, m.upsertErr
	}
	if _, ok := m.rowByFID[fid]; !ok {
		row := &store.CommanderRow{
			ID:          uuid.New(),
			FID:         fid,
			CmdrName:    name,
			Platform:    platform,
			FirstSeenAt: time.Now(),
			LastSeenAt:  time.Now(),
		}
		// In the post-Task-12 happy-path mode (used by
		// newPermissiveLinkTestRepo), the row is born approved + linked so
		// the access decision succeeds without the test having to seed one.
		if m.defaultApproved {
			id := uuid.New()
			row.Approved = true
			row.AuthentikUserID = &id
		}
		m.rowByFID[fid] = row
	} else {
		m.rowByFID[fid].CmdrName = name
		m.rowByFID[fid].LastSeenAt = time.Now()
	}
	return m.rowByFID[fid].ID, nil
}

func (m *linkTestRepo) GetCommanderAsAdmin(_ context.Context, fid string) (*store.CommanderRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	row, ok := m.rowByFID[fid]
	if !ok {
		if m.defaultApproved {
			defaultID := uuid.New()
			return &store.CommanderRow{
				ID:              uuid.New(),
				FID:             fid,
				Platform:        "frontier",
				FirstSeenAt:     time.Now(),
				LastSeenAt:      time.Now(),
				Approved:        true,
				AuthentikUserID: &defaultID,
			}, nil
		}
		return nil, store.ErrCommanderNotFound
	}
	// Return a copy so the caller can't mutate our seeded state by accident.
	rowCopy := *row
	if row.AuthentikUserID != nil {
		idCopy := *row.AuthentikUserID
		rowCopy.AuthentikUserID = &idCopy
	}
	return &rowCopy, nil
}

func (m *linkTestRepo) GetCommanderByAuthentikUserID(_ context.Context, userID uuid.UUID) (*store.CommanderRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	for _, row := range m.rowByFID {
		if row.Approved && row.AuthentikUserID != nil && *row.AuthentikUserID == userID {
			rowCopy := *row
			idCopy := *row.AuthentikUserID
			rowCopy.AuthentikUserID = &idCopy
			return &rowCopy, nil
		}
	}
	return nil, store.ErrCommanderNotFound
}

func (m *linkTestRepo) SetAuthentikLink(_ context.Context, fid string, userID *uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setLinkCalls = append(m.setLinkCalls, setLinkCall{FID: fid, UserID: userID})
	if m.setLinkErr != nil {
		return m.setLinkErr
	}
	if row, ok := m.rowByFID[fid]; ok {
		row.AuthentikUserID = userID
	}
	return nil
}

// upsertCallCount returns how many times UpsertCommander was invoked.
func (m *linkTestRepo) upsertCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.upsertCalls)
}

// setLinkCallCount returns how many times SetAuthentikLink was invoked.
func (m *linkTestRepo) setLinkCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.setLinkCalls)
}

// linkedUUID returns the AuthentikUserID currently stored for fid, or
// uuid.Nil if the row is missing or unlinked.
func (m *linkTestRepo) linkedUUID(fid string) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rowByFID[fid]
	if !ok || row.AuthentikUserID == nil {
		return uuid.Nil
	}
	return *row.AuthentikUserID
}

// ─── unimplemented (panics) ──────────────────────────────────────────────────

func (m *linkTestRepo) InsertEvents(_ context.Context, _ string, _ []store.JournalEvent) (int, int, error) {
	panic("not implemented")
}
func (m *linkTestRepo) RecentEvents(_ context.Context, _ string, _ int) ([]store.JournalEvent, error) {
	panic("not implemented")
}
func (m *linkTestRepo) EventsByType(_ context.Context, _ string, _ []string, _, _ time.Time) ([]store.JournalEvent, error) {
	panic("not implemented")
}
func (m *linkTestRepo) CurrentLocation(_ context.Context, _ string) (*store.LocationState, error) {
	panic("not implemented")
}
func (m *linkTestRepo) DeleteAllEvents(_ context.Context, _ string) error {
	panic("not implemented")
}
func (m *linkTestRepo) GetCommander(_ context.Context, _ string) (*store.CommanderRow, error) {
	panic("not implemented")
}
func (m *linkTestRepo) GetEventStats(_ context.Context, _ string) (*store.CommanderEventStats, error) {
	panic("not implemented")
}

// SetApproved was a panic-stub before Task 8; the admin Grant/Revoke
// flow calls it so it now mutates the seeded row when present. Tracks
// calls so tests can assert the sequence.
func (m *linkTestRepo) SetApproved(_ context.Context, fid string, approved bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setApprovedCalls = append(m.setApprovedCalls, setApprovedCall{FID: fid, Approved: approved})
	if m.setApprovedErr != nil {
		return m.setApprovedErr
	}
	row, ok := m.rowByFID[fid]
	if !ok {
		return store.ErrCommanderNotFound
	}
	row.Approved = approved
	return nil
}

// ListAllCommanders was a panic-stub before Task 8; the admin list
// endpoint and the Link conflicting-FID lookup call it.
func (m *linkTestRepo) ListAllCommanders(_ context.Context) ([]store.CommanderRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]store.CommanderRow, 0, len(m.rowByFID))
	for _, row := range m.rowByFID {
		rowCopy := *row
		if row.AuthentikUserID != nil {
			idCopy := *row.AuthentikUserID
			rowCopy.AuthentikUserID = &idCopy
		}
		out = append(out, rowCopy)
	}
	return out, nil
}

// ─── shadow user fakes ───────────────────────────────────────────────────────

// fakeShadowCreator returns a function-typed shadow-user creator suitable
// for assignment to Server.createShadowUser. callCount tracks invocations
// across goroutines (the WS callback path is single-threaded per request,
// but defensive locking matches the production seam).
type fakeShadowCreator struct {
	mu        sync.Mutex
	wantUUID  uuid.UUID
	wantErr   error
	callCount int
	calls     []shadowCall
}

type shadowCall struct {
	FID, CmdrName string
}

func (f *fakeShadowCreator) fn() func(ctx context.Context, fid, cmdrName string) (uuid.UUID, error) {
	return func(_ context.Context, fid, cmdrName string) (uuid.UUID, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.callCount++
		f.calls = append(f.calls, shadowCall{FID: fid, CmdrName: cmdrName})
		if f.wantErr != nil {
			return uuid.Nil, f.wantErr
		}
		return f.wantUUID, nil
	}
}

func (f *fakeShadowCreator) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// errAuthentikDown is a stand-in for transient Authentik failure used by
// the deny-closed path tests.
var errAuthentikDown = errors.New("authentik unreachable")
