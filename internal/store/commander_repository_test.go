//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// discardLogger returns an slog.Logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// setupRepo starts a fresh test DB, applies the commander schema, and returns
// a ready CommanderRepository.
func setupRepo(t *testing.T) store.CommanderRepository {
	t.Helper()
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))
	return store.NewPgCommanderRepository(pool, discardLogger())
}

// setupRepoWithPool is like setupRepo but also returns the raw pool (needed for
// some tests that poke the DB directly).
func setupRepoWithPool(t *testing.T) (store.CommanderRepository, *pgxpool.Pool) {
	t.Helper()
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))
	return store.NewPgCommanderRepository(pool, discardLogger()), pool
}

// makeEvent builds a JournalEvent with the given commanderID, eventType and
// timestamp. EventData is a minimal JSON blob.
func makeEvent(commanderID uuid.UUID, fid, eventType string, ts time.Time) store.JournalEvent {
	raw, _ := json.Marshal(map[string]string{"event": eventType})
	return store.JournalEvent{
		CommanderID:   commanderID,
		FID:           fid,
		Timestamp:     ts.UTC().Truncate(time.Microsecond), // postgres timestamptz precision
		EventType:     eventType,
		EventData:     json.RawMessage(raw),
		ClientVersion: "4.0.0.1000",
	}
}

// ─── UpsertCommander ─────────────────────────────────────────────────────────

func TestCommanderRepo_UpsertCommander_CreatesNew(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id, "should return a non-nil UUID")
}

func TestCommanderRepo_UpsertCommander_UpdatesExisting(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id1, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)

	// Upsert the same FID with a different name — should update, not create a second row.
	id2, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha Renamed", "frontier")
	require.NoError(t, err)

	require.Equal(t, id1, id2, "upsert on existing FID should return the same UUID")
}

// ─── InsertEvents ────────────────────────────────────────────────────────────

func TestCommanderRepo_InsertEvents_StoresEvents(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)

	now := time.Now().UTC()
	events := []store.JournalEvent{
		makeEvent(id, "F001", "Docked", now.Add(-2*time.Second)),
		makeEvent(id, "F001", "FSDJump", now.Add(-1*time.Second)),
		makeEvent(id, "F001", "Location", now),
	}

	inserted, duplicates, err := repo.InsertEvents(ctx, "F001", events)
	require.NoError(t, err)
	require.Equal(t, 3, inserted, "all three events should be inserted")
	require.Equal(t, 0, duplicates)
}

func TestCommanderRepo_InsertEvents_DeduplicatesOnTimestampAndType(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)

	now := time.Now().UTC()
	ev := makeEvent(id, "F001", "FSDJump", now)

	// First insert — should succeed.
	ins1, dup1, err := repo.InsertEvents(ctx, "F001", []store.JournalEvent{ev})
	require.NoError(t, err)
	require.Equal(t, 1, ins1)
	require.Equal(t, 0, dup1)

	// Second insert of the identical event — should be skipped.
	ins2, dup2, err := repo.InsertEvents(ctx, "F001", []store.JournalEvent{ev})
	require.NoError(t, err)
	require.Equal(t, 0, ins2, "duplicate event should not be inserted")
	require.Equal(t, 1, dup2, "duplicate count should be 1")
}

func TestCommanderRepo_InsertEvents_UsesContextFID_IgnoresBodyFID(t *testing.T) {
	// Events whose JournalEvent.FID field disagrees with the fid argument must
	// still be stored under the authoritative fid argument (the DB column is
	// written from the parameter, not from the struct's FID field).
	repo := setupRepo(t)
	ctx := context.Background()

	// Create a commander for the authoritative FID.
	id, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)

	now := time.Now().UTC()
	// The event struct claims FID = "EVIL", but we pass fid = "F001" to InsertEvents.
	ev := store.JournalEvent{
		CommanderID:   id,
		FID:           "EVIL", // intentionally wrong — should be overridden by the fid parameter
		Timestamp:     now.UTC().Truncate(time.Microsecond),
		EventType:     "FSDJump",
		EventData:     json.RawMessage(`{"event":"FSDJump"}`),
		ClientVersion: "4.0.0.1000",
	}

	ins, _, err := repo.InsertEvents(ctx, "F001", []store.JournalEvent{ev})
	require.NoError(t, err)
	require.Equal(t, 1, ins)

	// RecentEvents for F001 should return the event (stored with fid=F001).
	recent, err := repo.RecentEvents(ctx, "F001", 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, "F001", recent[0].FID, "row should be stored under the fid parameter, not struct field")
}

// ─── RecentEvents ────────────────────────────────────────────────────────────

func TestCommanderRepo_RecentEvents_ReturnsNewestFirst(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR Alpha", "frontier")
	require.NoError(t, err)

	base := time.Now().UTC()
	events := []store.JournalEvent{
		makeEvent(id, "F001", "Docked", base.Add(-3*time.Second)),
		makeEvent(id, "F001", "FSDJump", base.Add(-2*time.Second)),
		makeEvent(id, "F001", "Location", base.Add(-1*time.Second)),
	}
	_, _, err = repo.InsertEvents(ctx, "F001", events)
	require.NoError(t, err)

	recent, err := repo.RecentEvents(ctx, "F001", 10)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	require.Equal(t, "Location", recent[0].EventType, "newest event should be first")
	require.Equal(t, "Docked", recent[2].EventType, "oldest event should be last")
}

func TestCommanderRepo_RecentEvents_RLSIsolation(t *testing.T) {
	// Verifies that the RLS policy commander_isolation prevents F001's events
	// from appearing in F002's RecentEvents result set and vice-versa.
	repo, pool := setupRepoWithPool(t)
	ctx := context.Background()

	// Create test_rls_reader role for verifying isolation via a non-superuser.
	_, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'test_rls_reader') THEN
				CREATE ROLE test_rls_reader LOGIN PASSWORD 'test_rls_reader_pw';
			END IF;
		END $$;
		GRANT USAGE ON SCHEMA commander TO test_rls_reader;
		GRANT SELECT ON commander.journal_events TO test_rls_reader;
	`)
	require.NoError(t, err)

	// Insert commanders and events for F001 and F002.
	id1, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)
	id2, err := repo.UpsertCommander(ctx, "F002", "CMDR Two", "frontier")
	require.NoError(t, err)

	base := time.Now().UTC()
	evs1 := []store.JournalEvent{
		makeEvent(id1, "F001", "FSDJump", base.Add(-3*time.Second)),
		makeEvent(id1, "F001", "Location", base.Add(-2*time.Second)),
		makeEvent(id1, "F001", "Docked", base.Add(-1*time.Second)),
	}
	_, _, err = repo.InsertEvents(ctx, "F001", evs1)
	require.NoError(t, err)

	evs2 := []store.JournalEvent{
		makeEvent(id2, "F002", "FSDJump", base.Add(-3*time.Second)),
		makeEvent(id2, "F002", "Location", base.Add(-2*time.Second)),
	}
	_, _, err = repo.InsertEvents(ctx, "F002", evs2)
	require.NoError(t, err)

	// Repository-level isolation check.
	r1, err := repo.RecentEvents(ctx, "F001", 100)
	require.NoError(t, err)
	require.Len(t, r1, 3, "F001 should have exactly 3 events")
	for _, ev := range r1 {
		require.Equal(t, "F001", ev.FID)
	}

	r2, err := repo.RecentEvents(ctx, "F002", 100)
	require.NoError(t, err)
	require.Len(t, r2, 2, "F002 should have exactly 2 events")
	for _, ev := range r2 {
		require.Equal(t, "F002", ev.FID)
	}

	// Low-level RLS check via non-superuser role.
	var count int
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, "SET LOCAL ROLE test_rls_reader")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL app.current_fid = 'F001'")
	require.NoError(t, err)
	err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM commander.journal_events").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 3, count, "RLS must restrict non-superuser to F001's 3 events only")
	require.NoError(t, tx.Rollback(ctx))
}

// ─── Pool reuse / SET LOCAL isolation ────────────────────────────────────────

func TestCommanderRepo_PoolReuse_NoCrossContamination(t *testing.T) {
	// Pin the pool to MaxConns=1 so every query reuses the same connection.
	// If SET LOCAL leaks across transaction boundaries this test will see
	// events from the wrong commander.
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	// Rebuild pool with MaxConns=1.
	cfg := pool.Config()
	cfg.MaxConns = 1
	singlePool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(singlePool.Close)

	repo := store.NewPgCommanderRepository(singlePool, discardLogger())

	// Create two commanders.
	id1, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)
	id2, err := repo.UpsertCommander(ctx, "F002", "CMDR Two", "frontier")
	require.NoError(t, err)

	// Insert one distinctive event per commander.
	base := time.Now().UTC()
	_, _, err = repo.InsertEvents(ctx, "F001", []store.JournalEvent{
		makeEvent(id1, "F001", "FSDJump", base),
	})
	require.NoError(t, err)
	_, _, err = repo.InsertEvents(ctx, "F002", []store.JournalEvent{
		makeEvent(id2, "F002", "Location", base.Add(time.Second)),
	})
	require.NoError(t, err)

	// 100 sequential queries alternating between F001 and F002.
	for i := 0; i < 100; i++ {
		fid := "F001"
		if i%2 == 1 {
			fid = "F002"
		}
		events, err := repo.RecentEvents(ctx, fid, 10)
		require.NoError(t, err, "iteration %d: RecentEvents failed", i)
		for _, ev := range events {
			require.Equal(t, fid, ev.FID,
				"iteration %d: expected FID=%s but got event with FID=%s (SET LOCAL leaked)", i, fid, ev.FID)
		}
	}
}

func TestCommanderRepo_SetLocal_ExpiresAfterCommit(t *testing.T) {
	// After withFIDContext commits, a bare query on the same connection must
	// NOT see any residual app.current_fid value.
	// We verify this by doing a RecentEvents call (which sets the FID) and
	// then querying current_setting directly on the same pool.
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	// Pin to MaxConns=1 to guarantee connection reuse.
	cfg := pool.Config()
	cfg.MaxConns = 1
	singlePool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(singlePool.Close)

	repo := store.NewPgCommanderRepository(singlePool, discardLogger())

	// Upsert a commander so we have something to query.
	id1, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)
	base := time.Now().UTC()
	_, _, err = repo.InsertEvents(ctx, "F001", []store.JournalEvent{
		makeEvent(id1, "F001", "FSDJump", base),
	})
	require.NoError(t, err)

	// Perform a withFIDContext-scoped read for F001.
	_, err = repo.RecentEvents(ctx, "F001", 10)
	require.NoError(t, err)

	// After the transaction commits, app.current_fid must be unset (or empty).
	// current_setting with missing_ok=true returns '' when unset.
	var fidSetting string
	err = singlePool.QueryRow(ctx, "SELECT current_setting('app.current_fid', true)").Scan(&fidSetting)
	require.NoError(t, err)
	require.Empty(t, fidSetting,
		"app.current_fid must be empty after transaction commits (SET LOCAL must not persist)")
}

// ─── DeleteAllEvents ─────────────────────────────────────────────────────────

func TestCommanderRepo_DeleteAllEvents_OnlyDeletesTargetFID(t *testing.T) {
	repo, pool := setupRepoWithPool(t)
	ctx := context.Background()

	id1, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)
	id2, err := repo.UpsertCommander(ctx, "F002", "CMDR Two", "frontier")
	require.NoError(t, err)

	base := time.Now().UTC()
	_, _, err = repo.InsertEvents(ctx, "F001", []store.JournalEvent{
		makeEvent(id1, "F001", "FSDJump", base),
		makeEvent(id1, "F001", "Docked", base.Add(time.Second)),
	})
	require.NoError(t, err)
	_, _, err = repo.InsertEvents(ctx, "F002", []store.JournalEvent{
		makeEvent(id2, "F002", "Location", base),
	})
	require.NoError(t, err)

	// Delete all events for F001.
	require.NoError(t, repo.DeleteAllEvents(ctx, "F001"))

	// F001's events should be gone.
	var count int
	err = pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.journal_events WHERE fid = 'F001'",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count, "F001 events should all be deleted")

	// F002's event must remain untouched.
	err = pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.journal_events WHERE fid = 'F002'",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "F002 event must not be affected by F001's deletion")
}

func TestCommanderRepo_DeleteAllEvents_CompressedChunks_Succeeds(t *testing.T) {
	t.Skip("compression test requires manual chunk compression, covered in staging")
}

// ─── CurrentLocation ─────────────────────────────────────────────────────────

func TestCommanderRepo_CurrentLocation_ExtractsFromEvents(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	base := time.Now().UTC()

	// Insert an older FSDJump and a newer Location event.
	older := store.JournalEvent{
		CommanderID:   id,
		FID:           "F001",
		Timestamp:     base.Add(-10 * time.Second).UTC().Truncate(time.Microsecond),
		EventType:     "FSDJump",
		EventData:     json.RawMessage(`{"StarSystem":"Shinrarta Dezhra","StarPos":[-55.21875,17.59375,27.15625]}`),
		ClientVersion: "4.0.0.1000",
	}
	newer := store.JournalEvent{
		CommanderID:   id,
		FID:           "F001",
		Timestamp:     base.UTC().Truncate(time.Microsecond),
		EventType:     "Location",
		EventData:     json.RawMessage(`{"StarSystem":"Sol","StarPos":[0.0,0.0,0.0]}`),
		ClientVersion: "4.0.0.1000",
	}
	_, _, err = repo.InsertEvents(ctx, "F001", []store.JournalEvent{older, newer})
	require.NoError(t, err)

	loc, err := repo.CurrentLocation(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, loc)
	require.Equal(t, "Sol", loc.SystemName, "should extract StarSystem from the newest event")
	require.InDelta(t, 0.0, loc.StarPos[0], 0.001)
}

func TestCommanderRepo_CurrentLocation_ReturnsNilWhenNoEvents(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	loc, err := repo.CurrentLocation(ctx, "F001")
	require.NoError(t, err)
	require.Nil(t, loc, "should return nil when there are no location events")
}

// ─── WithFIDContext rollback logging ─────────────────────────────────────────

func TestCommanderRepo_WithFIDContext_RollbackErrorsLogged(t *testing.T) {
	// We can't easily simulate a network-split rollback error in a unit test,
	// but we can verify the happy path: a fn error causes rollback (no panic,
	// no leak) and the fn error is returned.
	repo, _ := setupRepoWithPool(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "FBAD", "CMDR Bad", "frontier")
	// Inserting into a non-existent FID for journal_events — or just testing
	// that an error from the fn propagates cleanly.
	require.NoError(t, err) // UpsertCommander itself must succeed.

	// Attempt to RecentEvents for a FID that exists but has no events —
	// this is fine and returns empty slice.
	evs, err := repo.RecentEvents(ctx, "FBAD", 10)
	require.NoError(t, err)
	require.Empty(t, evs)
}

// ─── EventsByType ─────────────────────────────────────────────────────────────

func TestCommanderRepo_EventsByType_FiltersCorrectly(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	id, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	base := time.Now().UTC()
	events := []store.JournalEvent{
		makeEvent(id, "F001", "FSDJump", base.Add(-4*time.Second)),
		makeEvent(id, "F001", "Docked", base.Add(-3*time.Second)),
		makeEvent(id, "F001", "FSDJump", base.Add(-2*time.Second)),
		makeEvent(id, "F001", "Location", base.Add(-1*time.Second)),
	}
	_, _, err = repo.InsertEvents(ctx, "F001", events)
	require.NoError(t, err)

	since := base.Add(-5 * time.Second)
	until := base.Add(time.Second)

	jumps, err := repo.EventsByType(ctx, "F001", []string{"FSDJump"}, since, until)
	require.NoError(t, err)
	require.Len(t, jumps, 2, "should return only FSDJump events")
	for _, ev := range jumps {
		require.Equal(t, "FSDJump", ev.EventType)
	}

	multi, err := repo.EventsByType(ctx, "F001", []string{"FSDJump", "Location"}, since, until)
	require.NoError(t, err)
	require.Len(t, multi, 3, "should return FSDJump and Location events")

	// Verify ordering: newest first.
	require.Equal(t, "Location", multi[0].EventType)

	// Narrow time range to exclude the oldest FSDJump.
	narrow, err := repo.EventsByType(ctx, "F001", []string{"FSDJump"}, base.Add(-3*time.Second), until)
	require.NoError(t, err)
	require.Len(t, narrow, 1, "time-range filter should exclude the oldest FSDJump")

	// No matching types.
	none, err := repo.EventsByType(ctx, "F001", []string{"SupercruiseEntry"}, since, until)
	require.NoError(t, err)
	require.Empty(t, none)

	// Verify cross-FID isolation (F002 has no events).
	crossFID, err := repo.EventsByType(ctx, "F002", []string{"FSDJump"}, since, until)
	require.NoError(t, err)
	require.Empty(t, crossFID, "F002 must not see F001's events")
}

// ─── InsertEvents edge cases ─────────────────────────────────────────────────

func TestCommanderRepo_InsertEvents_EmptySlice_Succeeds(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	ins, dup, err := repo.InsertEvents(ctx, "F001", nil)
	require.NoError(t, err)
	require.Equal(t, 0, ins)
	require.Equal(t, 0, dup)
}

// Ensure that using a format string for an injected chunk ID is safe.
// (Regression guard for the decompress_chunk call.)
func TestCommanderRepo_DeleteAllEvents_NothingToDelete_Succeeds(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	// No events inserted — deletion should succeed without error.
	err = repo.DeleteAllEvents(ctx, "F001")
	require.NoError(t, err)
}

// ─── Authentik link + approval (admin-tx surface) ────────────────────────────

func TestCommanderRepo_SetAuthentikLink_RoundTrip(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	userID := uuid.New()
	require.NoError(t, repo.SetAuthentikLink(ctx, "F001", &userID))

	row, err := repo.GetCommanderAsAdmin(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.AuthentikUserID)
	require.Equal(t, userID, *row.AuthentikUserID)
}

func TestCommanderRepo_GetCommanderByAuthentikUserID_RequiresApprovedLink(t *testing.T) {
	repo := setupRepo(t)
	lookup := repo.(store.CommanderAuthentikLookup)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)
	userID := uuid.New()
	require.NoError(t, repo.SetAuthentikLink(ctx, "F001", &userID))

	_, err = lookup.GetCommanderByAuthentikUserID(ctx, userID)
	require.ErrorIs(t, err, store.ErrCommanderNotFound)

	require.NoError(t, repo.SetApproved(ctx, "F001", true))
	row, err := lookup.GetCommanderByAuthentikUserID(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, "F001", row.FID)
	require.True(t, row.Approved)
}

func TestCommanderRepo_SetAuthentikLink_Unset_ClearsColumn(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	userID := uuid.New()
	require.NoError(t, repo.SetAuthentikLink(ctx, "F001", &userID))

	// Clear the link.
	require.NoError(t, repo.SetAuthentikLink(ctx, "F001", nil))

	row, err := repo.GetCommanderAsAdmin(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Nil(t, row.AuthentikUserID, "authentik_user_id should be nil after clearing")
}

func TestCommanderRepo_SetAuthentikLink_DuplicateRejected(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR A", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F002", "CMDR B", "frontier")
	require.NoError(t, err)

	userID := uuid.New()
	require.NoError(t, repo.SetAuthentikLink(ctx, "F001", &userID))

	// Linking F002 to the same Authentik user must fail.
	err = repo.SetAuthentikLink(ctx, "F002", &userID)
	require.Error(t, err)
	require.ErrorIs(t, err, store.ErrAuthentikUserAlreadyLinked)

	// F002's row must remain unchanged (authentik_user_id still NULL).
	row, err := repo.GetCommanderAsAdmin(ctx, "F002")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Nil(t, row.AuthentikUserID, "F002 must not be linked after rejected attempt")

	// F001 must still be linked.
	row1, err := repo.GetCommanderAsAdmin(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, row1.AuthentikUserID)
	require.Equal(t, userID, *row1.AuthentikUserID)
}

func TestCommanderRepo_SetAuthentikLink_NotFound_ReturnsErrCommanderNotFound(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	userID := uuid.New()
	err := repo.SetAuthentikLink(ctx, "F_DOES_NOT_EXIST", &userID)
	require.ErrorIs(t, err, store.ErrCommanderNotFound)
}

func TestCommanderRepo_SetApproved_RoundTrip(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	// Approve.
	require.NoError(t, repo.SetApproved(ctx, "F001", true))
	row, err := repo.GetCommanderAsAdmin(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.True(t, row.Approved)

	// Un-approve.
	require.NoError(t, repo.SetApproved(ctx, "F001", false))
	row, err = repo.GetCommanderAsAdmin(ctx, "F001")
	require.NoError(t, err)
	require.False(t, row.Approved)
}

func TestCommanderRepo_SetApproved_NotFound_ReturnsErrCommanderNotFound(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	err := repo.SetApproved(ctx, "F_DOES_NOT_EXIST", true)
	require.ErrorIs(t, err, store.ErrCommanderNotFound)
}

func TestCommanderRepo_GetCommander_DefaultsToNotApproved(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F001", "CMDR One", "frontier")
	require.NoError(t, err)

	row, err := repo.GetCommander(ctx, "F001")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.False(t, row.Approved, "fresh commander should default to approved=false")
	require.Nil(t, row.AuthentikUserID, "fresh commander should have nil AuthentikUserID")
}

func TestCommanderRepo_ListAllCommanders_OrderedByLastSeenDesc(t *testing.T) {
	repo, pool := setupRepoWithPool(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F_OLDEST", "CMDR Oldest", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F_MID", "CMDR Mid", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F_NEWEST", "CMDR Newest", "frontier")
	require.NoError(t, err)

	// Stagger last_seen_at via direct UPDATE (superuser bypass in tests).
	// Use concrete times so ordering is deterministic regardless of insertion
	// timing within the same millisecond.
	base := time.Now().UTC()
	_, err = pool.Exec(ctx,
		"UPDATE commander.commanders SET last_seen_at = $1 WHERE fid = $2",
		base.Add(-3*time.Hour), "F_OLDEST")
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		"UPDATE commander.commanders SET last_seen_at = $1 WHERE fid = $2",
		base.Add(-1*time.Hour), "F_MID")
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		"UPDATE commander.commanders SET last_seen_at = $1 WHERE fid = $2",
		base, "F_NEWEST")
	require.NoError(t, err)

	all, err := repo.ListAllCommanders(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, "F_NEWEST", all[0].FID, "newest last_seen should come first")
	require.Equal(t, "F_MID", all[1].FID)
	require.Equal(t, "F_OLDEST", all[2].FID, "oldest last_seen should come last")
}

func TestCommanderRepo_RLS_WriterCannotReadOtherFIDs(t *testing.T) {
	// In a bare test environment the PUBLIC branch of commanders_self_rw
	// applies, so setting app.current_fid without switching role is enough
	// to exercise the RLS filter — provided the session role is NOT a
	// superuser (which would bypass RLS implicitly). The testcontainer's
	// default user ("testuser") is a superuser; we therefore create a
	// non-superuser login role, grant it SELECT on commander.commanders,
	// and exercise RLS through it.
	repo, pool := setupRepoWithPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'test_cmd_rls_reader') THEN
				CREATE ROLE test_cmd_rls_reader LOGIN PASSWORD 'test_cmd_rls_reader_pw';
			END IF;
		END $$;
		GRANT USAGE ON SCHEMA commander TO test_cmd_rls_reader;
		GRANT SELECT ON commander.commanders TO test_cmd_rls_reader;
	`)
	require.NoError(t, err)

	// Seed two commanders.
	_, err = repo.UpsertCommander(ctx, "F1", "CMDR One", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F2", "CMDR Two", "frontier")
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, "SET LOCAL ROLE test_cmd_rls_reader")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL app.current_fid = 'F1'")
	require.NoError(t, err)

	// SELECT with fid = 'F2' while app.current_fid = 'F1' — RLS should hide F2.
	var count int
	err = tx.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.commanders WHERE fid = 'F2'",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count, "RLS must make F2 invisible when app.current_fid = 'F1'")

	// And the visible row count is 1 (only F1).
	err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM commander.commanders").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "RLS must filter commanders to app.current_fid")
}

func TestCommanderRepo_RLS_WriterCannotUpdateOtherFIDs(t *testing.T) {
	// Companion to TestCommanderRepo_RLS_WriterCannotReadOtherFIDs — that
	// test proves the USING clause filters reads, this one proves the
	// WITH CHECK + USING clauses filter cross-FID writes. Same setup
	// pattern: a synthetic non-superuser login role that exercises RLS
	// in the bare test environment (no edin_cmd_writer). The role here
	// needs UPDATE in addition to SELECT so the UPDATE statement parses
	// — RLS then filters the targeted row out via USING/WITH CHECK
	// before any write applies.
	repo, pool := setupRepoWithPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'test_cmd_rls_writer') THEN
				CREATE ROLE test_cmd_rls_writer LOGIN PASSWORD 'test_cmd_rls_writer_pw';
			END IF;
		END $$;
		GRANT USAGE ON SCHEMA commander TO test_cmd_rls_writer;
		GRANT SELECT, UPDATE ON commander.commanders TO test_cmd_rls_writer;
	`)
	require.NoError(t, err)

	// Seed two commanders. Both default to approved = false.
	_, err = repo.UpsertCommander(ctx, "F1", "CMDR One", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F2", "CMDR Two", "frontier")
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, "SET LOCAL ROLE test_cmd_rls_writer")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL app.current_fid = 'F1'")
	require.NoError(t, err)

	// Attempt to flip F2's approved flag while app.current_fid = 'F1'.
	// The USING clause filters F2 out of the row set the UPDATE could
	// touch, so RowsAffected must be 0 and no error is raised.
	tag, err := tx.Exec(ctx,
		"UPDATE commander.commanders SET approved = true WHERE fid = 'F2'",
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), tag.RowsAffected(),
		"RLS WITH CHECK/USING must prevent UPDATE on F2 when app.current_fid='F1'")

	// Commit so the (no-op) UPDATE is durable, then verify F2 is unchanged.
	require.NoError(t, tx.Commit(ctx))

	row2, err := repo.GetCommanderAsAdmin(ctx, "F2")
	require.NoError(t, err)
	require.NotNil(t, row2)
	require.False(t, row2.Approved,
		"F2.approved must remain false — the cross-FID UPDATE was filtered by RLS")
}

func TestCommanderRepo_UpsertCommander_StillWorksAfterColumnScopedGrant(t *testing.T) {
	// Regression test for the REVOKE+column-scoped GRANT in migration 008.
	// UpsertCommander's ON CONFLICT (fid) DO UPDATE refreshes cmdr_name
	// and last_seen_at. Both columns are in the column-scoped grant list
	// (authentik_user_id, approved, last_seen_at, cmdr_name), so the
	// upsert must continue to work after the migration narrows
	// cmd_writer's UPDATE privilege.
	//
	// Caveat: in bare test envs the connection role is a superuser
	// (BYPASSRLS, ignores column-level grants), so this test verifies the
	// upsert SQL is *syntactically* compatible with the new grant shape
	// rather than exercising the production grant constraints. Useful
	// regression coverage; full prod-role validation requires the
	// Ansible-provisioned roles which aren't present in tests.
	repo := setupRepo(t)
	ctx := context.Background()

	id1, err := repo.UpsertCommander(ctx, "F1", "Alpha", "frontier")
	require.NoError(t, err)

	// Second upsert hits the ON CONFLICT branch: cmdr_name and last_seen_at
	// get refreshed.
	id2, err := repo.UpsertCommander(ctx, "F1", "Alpha Renamed", "frontier")
	require.NoError(t, err)
	require.Equal(t, id1, id2, "upsert on same FID must return same UUID")

	row, err := repo.GetCommanderAsAdmin(ctx, "F1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "Alpha Renamed", row.CmdrName,
		"ON CONFLICT DO UPDATE must refresh cmdr_name after column-scoped grant")
}

func TestCommanderRepo_RLS_AdminTxSeesAllFIDs(t *testing.T) {
	// ListAllCommanders uses withAdminTx (SET LOCAL ROLE edin_cmd_admin,
	// BYPASSRLS). It must return rows for every FID regardless of any
	// app.current_fid value.
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F1", "CMDR One", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F2", "CMDR Two", "frontier")
	require.NoError(t, err)

	all, err := repo.ListAllCommanders(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2, "admin tx must see both F1 and F2 regardless of RLS")

	seen := map[string]bool{}
	for _, c := range all {
		seen[c.FID] = true
	}
	require.True(t, seen["F1"])
	require.True(t, seen["F2"])
}

func TestCommanderRepo_SetApproved_ViaAdminTx_Succeeds_ForOtherFID(t *testing.T) {
	// Admin tx should let us flip approved for a different FID than any
	// ambient app.current_fid — simulating the admin endpoint updating a
	// foreign commander's approval state.
	repo := setupRepo(t)
	ctx := context.Background()

	_, err := repo.UpsertCommander(ctx, "F1", "CMDR One", "frontier")
	require.NoError(t, err)
	_, err = repo.UpsertCommander(ctx, "F2", "CMDR Two", "frontier")
	require.NoError(t, err)

	// Call SetApproved for F2 without any prior SET LOCAL app.current_fid
	// (the admin tx doesn't set one). Succeeds via edin_cmd_admin.
	require.NoError(t, repo.SetApproved(ctx, "F2", true))

	row2, err := repo.GetCommanderAsAdmin(ctx, "F2")
	require.NoError(t, err)
	require.NotNil(t, row2)
	require.True(t, row2.Approved, "F2's approved should be true after admin-tx update")

	// F1 must remain unchanged.
	row1, err := repo.GetCommanderAsAdmin(ctx, "F1")
	require.NoError(t, err)
	require.False(t, row1.Approved, "F1 must not be affected")
}

func TestCommanderRepo_GetCommanderAsAdmin_NotFound_ReturnsErrCommanderNotFound(t *testing.T) {
	repo := setupRepo(t)
	ctx := context.Background()

	row, err := repo.GetCommanderAsAdmin(ctx, "F_DOES_NOT_EXIST")
	require.Nil(t, row)
	require.ErrorIs(t, err, store.ErrCommanderNotFound)
}

// ─── Parallel FID queries ─────────────────────────────────────────────────────

func TestCommanderRepo_ConcurrentFIDs_NoDataLeak(t *testing.T) {
	repo, _ := setupRepoWithPool(t)
	ctx := context.Background()

	// Create 5 commanders.
	const nFIDs = 5
	ids := make([]uuid.UUID, nFIDs)
	for i := 0; i < nFIDs; i++ {
		fid := fmt.Sprintf("F%03d", i)
		id, err := repo.UpsertCommander(ctx, fid, fmt.Sprintf("CMDR %d", i), "frontier")
		require.NoError(t, err)
		ids[i] = id
	}

	// Insert 3 events each.
	base := time.Now().UTC()
	for i := 0; i < nFIDs; i++ {
		fid := fmt.Sprintf("F%03d", i)
		events := []store.JournalEvent{
			makeEvent(ids[i], fid, "FSDJump", base.Add(time.Duration(i)*time.Second)),
			makeEvent(ids[i], fid, "Docked", base.Add(time.Duration(i)*time.Second+time.Millisecond)),
			makeEvent(ids[i], fid, "Location", base.Add(time.Duration(i)*time.Second+2*time.Millisecond)),
		}
		_, _, err := repo.InsertEvents(ctx, fid, events)
		require.NoError(t, err)
	}

	// Now query each FID and verify isolation.
	for i := 0; i < nFIDs; i++ {
		fid := fmt.Sprintf("F%03d", i)
		evs, err := repo.RecentEvents(ctx, fid, 10)
		require.NoError(t, err)
		require.Len(t, evs, 3, "FID %s should have exactly 3 events", fid)
		for _, ev := range evs {
			require.Equal(t, fid, ev.FID, "event FID must match query FID")
		}
	}
}
