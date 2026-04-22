# First-Time Backfill Cap Plan

**Date:** 2026-04-17
**Goal:** Cap first-time sync at most-recent N events per commander. Live events flow through unaffected. Configurable via debug UI to include all history later.

---

## Architecture

New sync_state value `'skipped'`. On first sync per FID, events older than the Nth most recent get marked skipped and never sync unless the user opts in. Per-FID flag in SettingsService prevents re-running.

### Status machine

```
new event inserted → 'pending' (as today)
first sync for FID (if > N pending) → older events → 'skipped'
user clicks "Sync all history" → 'skipped' → 'pending', flag cleared
```

### Key contract

- `sync_status = 'skipped'` is **never** included in `getEventsPendingSync()` query.
- Backfill cap uses `journal_events.timestamp DESC` (game time), not `processed_at DESC` (DB insert time).
- Backfill cap runs **once** per FID per install, tracked by `SettingsService.backfill_complete_<fid>` bool.

---

## File Map

### Create

| File | Responsibility |
|------|----------------|
| `packages/edin-journal/test/storage/sync_repository_backfill_test.dart` | Unit tests for new SyncRepository methods |
| `packages/edin-journal/test/services/sync_service_backfill_test.dart` | Unit tests for SyncService backfill orchestration |
| `packages/edin-journal/test/integration/backfill_flow_test.dart` | Integration test: insert 20K events, apply cap, verify counts |

### Modify

| File | What changes |
|------|-------------|
| `packages/edin-journal/lib/src/storage/repositories/sync_repository.dart` | Add 5 new methods (see below) |
| `packages/edin-journal/lib/src/services/sync_service.dart` | Add `applyBackfillLimit()`, `DEFAULT_BACKFILL_LIMIT = 10000` |
| `packages/edin-journal/lib/src/core/sync_coordinator.dart` | Call `applyBackfillLimit()` once per FID per session |
| `lib/services/settings_service.dart` | Add `isBackfillComplete(fid)` / `setBackfillComplete(fid, value)` / `clearBackfillComplete(fid)` |
| `lib/ui/widgets/config/debug_settings_widget.dart` | Show "Skipped" count in sync status panel, add "Sync all history" button |

---

## Task 1: SyncRepository — New Methods (TDD)

**File:** `packages/edin-journal/lib/src/storage/repositories/sync_repository.dart`
**Test:** `packages/edin-journal/test/storage/sync_repository_backfill_test.dart`

### Methods to add

```dart
/// Count pending events for a FID (used to decide if backfill cap is needed).
Future<int> countPendingForFid(String fid)

/// Find the timestamp of the Nth most recent event for this FID.
/// Returns null if fewer than `keepCount` events exist.
/// Uses `timestamp DESC` (game time) not `processed_at`.
Future<String?> getBackfillCutoffTimestamp(String fid, int keepCount)

/// Mark all pending/retry events strictly older than `cutoffTimestamp` as 'skipped'.
/// Uses game `timestamp` not `processed_at`.
/// Returns count of rows affected.
Future<int> markEventsSkippedBeforeTimestamp(String fid, String cutoffTimestamp)

/// Count events in skipped status for this FID (for UI).
Future<int> countSkippedForFid(String fid)

/// Move all skipped events back to pending for this FID.
/// Used by "Sync all history" button.
/// Returns count of rows affected.
Future<int> unmarkSkippedForFid(String fid)
```

### Tests (write first, confirm fail, then implement)

```dart
group('countPendingForFid', () {
  test('returns 0 for FID with no events', () async {...});
  test('counts only pending and retry events', () async {
    // insert 10 pending, 5 synced, 3 retry, 2 failed → expect 13
  });
  test('isolates by FID', () async {...});
});

group('getBackfillCutoffTimestamp', () {
  test('returns null when events < keepCount', () async {...});
  test('returns timestamp of Nth most recent event', () async {
    // insert 100 events spanning 10 days
    // getBackfillCutoffTimestamp(fid, 10) → timestamp of 10th newest
  });
  test('ordered by timestamp DESC not processed_at', () async {
    // insert events with older timestamp but newer processed_at
    // confirm cutoff uses timestamp
  });
});

group('markEventsSkippedBeforeTimestamp', () {
  test('marks only events strictly older than cutoff', () async {...});
  test('does not touch synced events', () async {...});
  test('does not touch events from other FIDs', () async {...});
  test('returns correct count of rows affected', () async {...});
});

group('countSkippedForFid', () {
  test('returns 0 when no skipped events', () async {...});
  test('counts only skipped status', () async {...});
});

group('unmarkSkippedForFid', () {
  test('moves skipped back to pending', () async {...});
  test('clears retry_count and error_message', () async {...});
  test('isolates by FID', () async {...});
});
```

---

## Task 2: SettingsService — Per-FID Backfill Flag (TDD)

**File:** `lib/services/settings_service.dart`

### Methods to add

```dart
static const _backfillCompletePrefix = 'backfill_complete_';

bool isBackfillComplete(String fid) {
  return _prefs?.getBool('$_backfillCompletePrefix$fid') ?? false;
}

Future<void> setBackfillComplete(String fid, bool value) async {
  await _prefs?.setBool('$_backfillCompletePrefix$fid', value);
}

Future<void> clearBackfillComplete(String fid) async {
  await _prefs?.remove('$_backfillCompletePrefix$fid');
}
```

### Tests

Existing SettingsService has no tests — we'll skip comprehensive unit tests here and verify via the integration test. SharedPreferences is well-trodden territory.

---

## Task 3: SyncService — Orchestration Method (TDD)

**File:** `packages/edin-journal/lib/src/services/sync_service.dart`
**Test:** `packages/edin-journal/test/services/sync_service_backfill_test.dart`

### Additions

```dart
// After existing constants:
static const int DEFAULT_BACKFILL_LIMIT = 10000;

/// Apply the backfill cap for a FID if not already applied.
/// - Counts pending events for FID.
/// - If count > maxEvents, finds cutoff timestamp at maxEvents-th most recent event.
/// - Marks everything older as 'skipped'.
/// - Returns count of events marked skipped (0 if under cap or already applied).
///
/// Caller is responsible for reading and setting the "backfill complete" flag
/// — this method does NOT touch persistent state, so it's safe to test in isolation.
Future<int> applyBackfillLimit(String fid, int maxEvents) async {
  final pending = await _syncRepository.countPendingForFid(fid);
  if (pending <= maxEvents) return 0;

  final cutoff = await _syncRepository.getBackfillCutoffTimestamp(fid, maxEvents);
  if (cutoff == null) return 0;

  return await _syncRepository.markEventsSkippedBeforeTimestamp(fid, cutoff);
}
```

### Tests

Uses `MockSyncRepository` (generated via `mockito` or hand-rolled fake).

```dart
group('applyBackfillLimit', () {
  test('returns 0 when pending count is at or below limit', () async {
    // countPendingForFid → 5000, maxEvents = 10000 → returns 0
    // markEventsSkippedBeforeTimestamp NOT called
  });

  test('caps at maxEvents when pending exceeds limit', () async {
    // countPendingForFid → 20000
    // getBackfillCutoffTimestamp(fid, 10000) → '2026-04-10T11:30:26Z'
    // markEventsSkippedBeforeTimestamp(fid, '2026-04-10T11:30:26Z') → 10000
    // returns 10000
  });

  test('returns 0 if cutoff timestamp cannot be determined', () async {
    // countPendingForFid → 20000 (race condition: some events got deleted)
    // getBackfillCutoffTimestamp → null
    // returns 0
  });
});
```

---

## Task 4: SyncCoordinator — Integration (no test needed — exercised by integration test)

**File:** `packages/edin-journal/lib/src/core/sync_coordinator.dart`

### Changes

The coordinator doesn't know about SettingsService (it's in a sub-package). Instead, inject a callback that the main app wires up. Two clean options:

**Option A — Callback in constructor:**
```dart
class SyncCoordinator {
  final Future<bool> Function(String fid) isBackfillComplete;
  final Future<void> Function(String fid) markBackfillComplete;
  // ... inject via constructor
}
```

**Option B — Track per-session in memory:**
The simpler approach: track a `Set<String> _backfilledThisSession` in the coordinator. If FID not in set, call `applyBackfillLimit`, then add FID to set. This runs once per app launch per FID.

The persistent flag lives in SettingsService, checked by the journal_engine_service (which has access to both packages). So:

**Recommended: Hybrid**

```dart
// In journal_engine_service.dart, before starting the engine:
if (authService.commanderFid != null) {
  final fid = authService.commanderFid!;
  if (!SettingsService.instance.isBackfillComplete(fid)) {
    final skipped = await engine.applyBackfillLimit(fid, SyncService.DEFAULT_BACKFILL_LIMIT);
    await SettingsService.instance.setBackfillComplete(fid, true);
    _logger.info('Backfill cap applied: $skipped events skipped for FID $fid');
  }
}
```

This keeps SettingsService out of the edin-journal package (which has no dependency on lib/services/). The engine exposes `applyBackfillLimit` as a passthrough to `SyncService.applyBackfillLimit`.

### Changes needed in edin-journal

In `edin_journal_engine.dart`:
```dart
Future<int> applyBackfillLimit(String fid, int maxEvents) async {
  if (_syncCoordinator == null) return 0;
  return await _syncCoordinator!.applyBackfillLimit(fid, maxEvents);
}
```

In `sync_coordinator.dart`:
```dart
Future<int> applyBackfillLimit(String fid, int maxEvents) async {
  return await _syncService.applyBackfillLimit(fid, maxEvents);
}
```

---

## Task 5: JournalEngineService — Wire It In

**File:** `lib/services/journal_engine_service.dart`

After the engine is created and after auth state is known, before the sync coordinator's first trigger, call `applyBackfillLimit` if the flag isn't set. Place this inside the existing `authStateChanges` listener:

```dart
authService.authStateChanges.listen((isAuthenticated) async {
  if (isAuthenticated && _engine != null) {
    _logger.info('Auth state changed to authenticated');
    final fid = authService.commanderFid;
    if (fid != null) {
      if (!SettingsService.instance.isBackfillComplete(fid)) {
        _logger.info('First sync for FID $fid — applying backfill cap...');
        final skipped = await _engine!.applyBackfillLimit(fid, 10000);
        await SettingsService.instance.setBackfillComplete(fid, true);
        _logger.info('Backfill cap applied: $skipped events skipped');
      }
    }
    _engine!.initializeSyncIfReady();
  }
});
```

---

## Task 6: Debug UI — Show Skipped Count + "Sync All History"

**File:** `lib/ui/widgets/config/debug_settings_widget.dart`

1. Update `_loadSyncStats()` to include skipped count.
2. Add row to sync status panel: `Skipped: N (first-time backfill cap)`
3. Add button in Debug Settings panel: "Sync all history" — confirms, then:
   - Calls new `AuthService.commanderFid` / `SyncRepository.unmarkSkippedForFid(fid)` via journal engine
   - `SettingsService.instance.clearBackfillComplete(fid)`
   - Refreshes stats

We'll add a passthrough method on `JournalEngineService`:
```dart
Future<int> unmarkSkippedEvents(String fid) async {
  return await _engine?.unmarkSkippedEvents(fid) ?? 0;
}
```

And expose it from `EDINJournalEngine` / `SyncCoordinator` down to `SyncRepository.unmarkSkippedForFid`.

---

## Task 7: Integration Test

**File:** `packages/edin-journal/test/integration/backfill_flow_test.dart`

Full end-to-end test using an in-memory SQLite database:

```dart
test('backfill cap correctly separates old from new events', () async {
  // Setup: 20 events across 20 hours, fid='F1234'
  // All marked pending initially
  
  // Act: call SyncService.applyBackfillLimit('F1234', 10)
  final skipped = await syncService.applyBackfillLimit('F1234', 10);
  
  // Assert:
  expect(skipped, 10);
  expect(await repo.countPendingForFid('F1234'), 10); // newest 10
  expect(await repo.countSkippedForFid('F1234'), 10); // oldest 10
  
  // Verify boundary: the pending events are the 10 most recent by timestamp
  final pending = await repo.getEventsPendingSync(fid: 'F1234', limit: 100);
  expect(pending.length, 10);
  expect(pending.first['timestamp'], greaterThan(cutoffTimestamp));
});

test('idempotency: second call is no-op', () async {
  // Given: 20 events, 10 already marked skipped
  final skipped2 = await syncService.applyBackfillLimit('F1234', 10);
  expect(skipped2, 0); // countPending now = 10, at cap, no action
});

test('unmarkSkipped restores all skipped events', () async {
  // Given: 20 events, 10 pending, 10 skipped
  final unskipped = await repo.unmarkSkippedForFid('F1234');
  expect(unskipped, 10);
  expect(await repo.countPendingForFid('F1234'), 20);
  expect(await repo.countSkippedForFid('F1234'), 0);
});

test('cap under limit does nothing', () async {
  // Given: 5 events, limit = 10
  final skipped = await syncService.applyBackfillLimit('F1234', 10);
  expect(skipped, 0);
});

test('live events added after backfill are not capped', () async {
  // Given: 20 events, cap applied → 10 pending, 10 skipped
  // When: 5 new events inserted as pending
  // Then: 15 pending (10 old + 5 new), 10 skipped
});

test('cross-FID isolation', () async {
  // Given: FID A has 20 events, FID B has 5 events
  // When: applyBackfillLimit for A only (limit=10)
  // Then: A has 10 pending + 10 skipped, B has 5 pending + 0 skipped
});
```

---

## Implementation Order (TDD)

1. **Task 1**: Write `sync_repository_backfill_test.dart` (all 5 method groups). Run → confirm all fail.
2. **Task 1**: Implement the 5 methods on `SyncRepository`. Run tests → all pass.
3. **Task 3**: Write `sync_service_backfill_test.dart`. Run → fail.
4. **Task 3**: Implement `SyncService.applyBackfillLimit`. Run tests → pass.
5. **Task 7**: Write `backfill_flow_test.dart`. Run → fail.
6. **Task 2, 4, 5, 6**: Implement SettingsService flag + engine/coordinator passthroughs + service wire-up + UI. Run integration test → pass.
7. Run full test suite → verify no regressions.
8. Launch app with `DEFAULT_BACKFILL_LIMIT = 10000`, verify live behaviour.

---

## Commits (atomic)

Each task = one commit:

1. `test(sync): add failing tests for backfill repository methods`
2. `feat(sync): add backfill methods to SyncRepository`
3. `test(sync): add failing tests for SyncService.applyBackfillLimit`
4. `feat(sync): add applyBackfillLimit to SyncService`
5. `test(sync): add backfill integration test`
6. `feat(sync): wire backfill cap into SyncCoordinator + EDINJournalEngine`
7. `feat(settings): add per-FID backfill complete flag`
8. `feat(journal-engine-service): apply backfill cap on first sync per FID`
9. `feat(debug-ui): show skipped count + 'Sync all history' button`

---

## Rollout

- Set `DEFAULT_BACKFILL_LIMIT = 10000` in code.
- Reset `backfill_complete_F2504` flag in existing SettingsService so the backfill runs on next launch.
- Verify via debug UI: "Skipped: 10,043".
- Click "Sync all history" → should unskip and drain to server.
