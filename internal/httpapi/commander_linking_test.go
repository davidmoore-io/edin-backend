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

	// SetAuthentikLink
	setLinkCalls []setLinkCall
	setLinkErr   error
}

type upsertCall struct {
	FID, Name, Platform string
}

type setLinkCall struct {
	FID    string
	UserID *uuid.UUID
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
		m.rowByFID[fid] = &store.CommanderRow{
			ID:          uuid.New(),
			FID:         fid,
			CmdrName:    name,
			Platform:    platform,
			FirstSeenAt: time.Now(),
			LastSeenAt:  time.Now(),
		}
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
func (m *linkTestRepo) SetApproved(_ context.Context, _ string, _ bool) error {
	panic("not implemented")
}
func (m *linkTestRepo) ListAllCommanders(_ context.Context) ([]store.CommanderRow, error) {
	panic("not implemented")
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
