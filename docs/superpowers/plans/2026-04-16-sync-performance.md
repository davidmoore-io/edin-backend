# Sync Performance Fix Plan

**Date:** 2026-04-16
**Problem:** Syncing 20K events takes 30+ minutes. Should take under 30 seconds.
**Goal:** Sub-30-second full history sync with correct failure handling.

---

## Root Cause Diagnosis — Ranked by Impact

### 1. No WAL Mode — SQLite Write Lock Blocks All Reads (DOMINANT)

**File:** `packages/edin-journal/lib/src/storage/sqlite_manager.dart`
**Function:** `initDatabase()` (line 105)

The database opens in default DELETE journal mode. Every write transaction (the 1000-event startup batch inserts) takes an **exclusive lock on the entire database file**. While that lock is held, sync queries (`getEventsPendingSync`) cannot run at all — they queue behind the write.

During startup reconstruction, ~20 batch transactions of 1000 events each hold the write lock for 50-200ms each. The sync timer fires every 5 seconds but its SELECT query parks in the sqflite queue behind the current batch, effectively stalling for the entire reconstruction period.

**Fix:** Add WAL pragma on database open:
```dart
onOpen: (db) async {
  await db.execute('PRAGMA journal_mode=WAL');
  await db.execute('PRAGMA synchronous=NORMAL');
  await db.execute('PRAGMA cache_size=-64000'); // 64MB
}
```

WAL mode allows concurrent readers while a writer holds the lock. Sync reads can proceed in parallel with startup writes.

**Impact:** Eliminates the primary blocking. Sync can start draining immediately even during reconstruction.

---

### 2. Serial Per-Event DB Ops in `_handleSyncFailure` (HIGH)

**File:** `packages/edin-journal/lib/src/services/sync_service.dart`
**Function:** `_handleSyncFailure()` (line ~223)

On any retriable failure of a 500-event batch, this function loops over every event and does 2 sequential `await` DB operations per event: `getRetryCount()` + `scheduleEventRetry()`. That's 1000 serialized DB round-trips through sqflite's single-threaded queue. At 1-2ms each = 1-2 seconds of pure DB queue time per failed batch. During startup contention, this balloons to 10-100 seconds.

**Fix:** Add batch methods to `sync_repository.dart`:

```dart
// New: fetch all retry counts in one query
Future<Map<String, int>> getRetryCountsBatch(List<String> uuids)

// New: schedule retries in one batch transaction  
Future<void> scheduleEventRetryBatch(Map<String, DateTime> uuidToNextRetry, String error)

// New: permanently fail in one batch transaction
Future<void> markEventsFailedPermanentlyBatch(List<String> uuids, String error)
```

Then rewrite `_handleSyncFailure` to use these 3 calls instead of 1000.

**Impact:** Failure path goes from 1-10 seconds to ~5ms.

---

### 3. Double JSON Parse per Event in HTTP Client (MEDIUM)

**File:** `packages/edin-journal/lib/src/network/http_client.dart`
**Function:** `uploadUniversalBatch()` (line ~44)

Every event's `raw_json` (stored as TEXT in SQLite) is `jsonDecode`'d into a Map, two keys are removed, then the whole batch is `jsonEncode`'d. That's 500 `jsonDecode` + 500 Map allocations + GC pressure per batch cycle.

**Fix:** Skip the parse entirely. Send `raw_json` as a string in the `event_data` field. The Go backend's `json.RawMessage` type accepts pre-encoded JSON strings directly:

```dart
wireEvents.add({
  'timestamp': e['timestamp'] as String? ?? '',
  'event': e['event_type'] as String? ?? '',
  'fid': fid,
  'commander_name': commanderName,
  'event_data': e['raw_json'] as String? ?? '{}',
});
```

The `jsonEncode` call will encode `event_data` as a JSON string (double-encoded). To avoid that, use `json.RawMessage`-compatible encoding by keeping it as a string but wrapping it properly — or accept the minor redundancy of `timestamp`/`event` keys in `event_data` and pass the raw JSON directly without parsing.

The cleanest approach: the Go backend already accepts `json.RawMessage` for `event_data`, which is just raw bytes. Send `raw_json` as-is by embedding it as already-encoded JSON. This requires using a custom JSON encoder or `dart:convert`'s `JsonUtf8Encoder` to avoid double-encoding.

Simplest practical fix: just don't strip the keys. The backend stores `event_data` verbatim and doesn't care about duplicate keys:

```dart
// Replace jsonDecode+strip+re-encode with direct passthrough
final rawJson = e['raw_json'] as String? ?? '{}';
wireEvents.add({
  'timestamp': e['timestamp'] as String? ?? '',
  'event': e['event_type'] as String? ?? '',
  'fid': fid,
  'commander_name': commanderName,
  'event_data': jsonDecode(rawJson), // single decode, no strip
});
```

Or even better, accumulate the wire JSON as a string buffer to avoid the decode entirely — but this is a larger refactor.

**Impact:** ~1-3ms savings per batch. Reduces GC pressure over 40 batches.

---

### 4. Immediate Sync on Coordinator Start (MEDIUM)

**File:** `packages/edin-journal/lib/src/core/sync_coordinator.dart`
**Function:** `startBackgroundSync()` (line 32)

Currently, the first sync fires only after the 5-second timer interval elapses. Add an immediate trigger:

```dart
void startBackgroundSync() {
  if (_isEnabled) return;
  _isEnabled = true;

  _checkInitialConnectivity().then((_) {
    if (_isOnline) _triggerSync(); // Immediate first sync
  });

  _connectivitySubscription = Connectivity().onConnectivityChanged.listen(/* ... */);
  _syncTimer = Timer.periodic(SyncService.SYNC_INTERVAL, (_) {
    if (_isOnline && !_isSyncing && _isEnabled) _triggerSync();
  });
}
```

**Impact:** Eliminates 0-5 second dead time after startup.

---

### 5. Verbose Logging During Reconstruction (MEDIUM)

**File:** `packages/edin-journal/lib/src/state_manager/game_state_manager.dart`
**Function:** `processEvent()` (line ~59)

15+ `_logger.info()` calls per event × 20K events = 300K log calls. String interpolation and `toIso8601String()` run on every call even if output is discarded. The `print()` calls (lines ~164, 165, 188, 189) emit console output for every live event.

**Fix:** Move per-event diagnostics to `_logger.finest()`. Remove or guard `print()` calls behind a debug flag. Keep `_logger.info()` only for errors and phase transitions.

**Impact:** 20-50% reduction in reconstruction time.

---

### 6. Missing Composite Index on sync_state (LOW — scales with data)

**File:** `packages/edin-journal/lib/src/storage/sqlite_manager.dart`
**Function:** `_onCreate()` and `_onUpgrade()`

Add covering index for the pending-events query:

```sql
CREATE INDEX IF NOT EXISTS idx_sync_state_pending 
ON sync_state(sync_status, next_retry_at);

CREATE INDEX IF NOT EXISTS idx_sync_state_uuid_status 
ON sync_state(event_uuid, sync_status);
```

**Impact:** Minor now. Important as data grows past 100K events.

---

### 7. Live Events Written Without Transaction (CORRECTNESS)

**File:** `packages/edin-journal/lib/src/storage/sqlite_manager.dart`
**Function:** `insertJournalEvent()` (static, line ~1071)

Live events (post-startup, from journal file watcher) are inserted into `journal_events` and `sync_state` as two separate non-transactional writes. If the app crashes between them, the event exists without a sync_state row and will never sync.

**Fix:** Wrap both inserts in a transaction:

```dart
static Future<void> insertJournalEvent(...) async {
  final db = await SqliteManager.instance.database;
  await db.transaction((txn) async {
    await txn.insert('journal_events', {...}, conflictAlgorithm: ConflictAlgorithm.ignore);
    await txn.insert('sync_state', {...}, conflictAlgorithm: ConflictAlgorithm.ignore);
  });
}
```

**Impact:** Correctness fix. No orphaned events.

---

## Implementation Order

| Priority | Fix | Files | Est. Time |
|----------|-----|-------|-----------|
| 1 | WAL mode | sqlite_manager.dart | 5 min |
| 2 | Batch failure handling | sync_repository.dart, sync_service.dart | 20 min |
| 3 | Immediate sync trigger | sync_coordinator.dart | 2 min |
| 4 | Skip double JSON parse | http_client.dart | 5 min |
| 5 | Reduce logging | game_state_manager.dart | 10 min |
| 6 | Add indexes | sqlite_manager.dart | 5 min |
| 7 | Transaction for live events | sqlite_manager.dart | 5 min |

---

## Expected Endstate

After all fixes:

| Metric | Before | After |
|--------|--------|-------|
| Time to sync 20K events | 30+ minutes | 10-30 seconds |
| Per-batch cycle | ~90 seconds (blocked) | ~30ms |
| Failure handling for 500-event batch | 1000 DB ops (~10s) | 3 DB ops (~5ms) |
| Startup DB contention | Complete read block | Concurrent reads via WAL |
| Live event safety | No transaction (crash risk) | Atomic transaction |
| Log noise during reconstruction | 300K info calls | ~500 info calls |

The sync pipeline becomes: query 500 pending events (~5ms) → serialize (~2ms) → POST to backend (~10ms via ngrok) → batch-mark synced (~5ms) → loop. At ~25ms per 500-event batch, 20K events drain in 40 batches × 25ms = **~1 second of execution time**, with realistic overhead bringing it to **10-30 seconds total**.
