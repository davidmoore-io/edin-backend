package httpapi

// commander_ingest_compat_test.go validates that the ingest endpoints accept
// payloads in the exact format the Flutter edin-client sends.
//
// The Flutter client (lib/services/edin_api_service.dart) sends:
//
//   Single event — POST /api/v1/ingest/event:
//     {"event": {"timestamp": <RFC3339>, "event": <type>, "fid": <fid>,
//                "commander_name": <name>, "event_data": {<raw ED fields>}}}
//
//   Batch — POST /api/v1/ingest/events:
//     {"events": [{"timestamp": ..., "event": ..., "fid": ...,
//                  "commander_name": ..., "event_data": {...}}, ...]}
//
// These tests are read-only — they do NOT modify the allowlist or handlers.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── payload builders ────────────────────────────────────────────────────────

// compatSingleBody builds the exact JSON shape the Flutter client sends to
// POST /api/v1/ingest/event.  eventData holds the raw ED journal fields.
func compatSingleBody(t *testing.T, eventType, ts, fid string, eventData map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"event": map[string]any{
			"timestamp":      ts,
			"event":          eventType,
			"fid":            fid,
			"commander_name": "Test CMDR",
			"event_data":     eventData,
		},
	})
	require.NoError(t, err)
	return string(b)
}

// compatBatchBody builds the exact JSON shape the Flutter client sends to
// POST /api/v1/ingest/events.
func compatBatchBody(t *testing.T, items []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"events": items})
	require.NoError(t, err)
	return string(b)
}

// compatBatchItem is a convenience helper that mirrors the Flutter client's
// uploadBatchEvents mapping in edin_api_service.dart.
func compatBatchItem(eventType, ts, fid string, eventData map[string]any) map[string]any {
	return map[string]any{
		"timestamp":      ts,
		"event":          eventType,
		"fid":            fid,
		"commander_name": "Test CMDR",
		"event_data":     eventData,
	}
}

// ─── realistic ED field sets ──────────────────────────────────────────────────

var fsdJumpFields = map[string]any{
	"StarSystem":    "Shinrarta Dezhra",
	"SystemAddress": float64(3932277478106),
	"StarPos":       []any{-55.21875, 65.1875, -4.15625},
	"Body":          "Shinrarta Dezhra",
	"BodyID":        float64(0),
	"BodyType":      "Star",
	"JumpDist":      14.852,
	"FuelUsed":      1.234,
	"FuelLevel":     28.766,
}

var locationFields = map[string]any{
	"StarSystem":    "Shinrarta Dezhra",
	"SystemAddress": float64(3932277478106),
	"StarPos":       []any{-55.21875, 65.1875, -4.15625},
	"Body":          "Shinrarta Dezhra",
	"BodyID":        float64(0),
	"BodyType":      "Star",
	"Docked":        false,
	"Taxi":          false,
	"Multicrew":     false,
	"InSRV":         false,
	"OnFoot":        false,
}

var dockedFields = map[string]any{
	"StationName": "Jameson Memorial",
	"StationType": "Orbis",
	"StarSystem":  "Shinrarta Dezhra",
	"SystemAddress": float64(3932277478106),
	"MarketID":    float64(128666762),
	"StationFaction": map[string]any{
		"Name":           "Pilots Federation Local Branch",
		"FactionState":   "None",
	},
	"StationGovernment": "$government_Democracy;",
	"StationAllegiance": "PilotsFederation",
	"StationServices":   []any{"dock", "autodock", "commodities", "outfitting", "shipyard"},
	"StationEconomy":    "$economy_HighTech;",
	"DistFromStarLS":    388.026,
	"Taxi":              false,
	"Multicrew":         false,
	"ActiveFine":        false,
}

// ─── Single-event compat tests ────────────────────────────────────────────────

func TestIngestCompat_SingleEvent_FSDJump(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	body := compatSingleBody(t, "FSDJump", validTimestamp(), "F1234", fsdJumpFields)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "FSDJump should be accepted; body: %s", rr.Body.String())

	var resp ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 1, resp.EventsWritten)
	assert.Equal(t, 0, resp.EventsDuplicated)

	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "FSDJump", repo.insertCalls[0].events[0].EventType)
}

func TestIngestCompat_SingleEvent_Location(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	body := compatSingleBody(t, "Location", validTimestamp(), "F1234", locationFields)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Location should be accepted; body: %s", rr.Body.String())

	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "Location", repo.insertCalls[0].events[0].EventType)
}

func TestIngestCompat_SingleEvent_Docked(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	body := compatSingleBody(t, "Docked", validTimestamp(), "F1234", dockedFields)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Docked should be accepted; body: %s", rr.Body.String())

	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "Docked", repo.insertCalls[0].events[0].EventType)
}

func TestIngestCompat_SingleEvent_UnknownType_Rejected(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	body := compatSingleBody(t, "UnknownEvent123", validTimestamp(), "F1234", map[string]any{"foo": "bar"})
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown event type")
	assert.Len(t, repo.insertCalls, 0, "no insert should occur for unknown type")
}

// ─── Batch compat tests ───────────────────────────────────────────────────────

func TestIngestCompat_Batch_MixedEvents(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 3
	srv := newIngestTestServer(t, repo)

	ts := validTimestamp()
	items := []map[string]any{
		compatBatchItem("FSDJump", ts, "F1234", fsdJumpFields),
		compatBatchItem("Location", ts, "F1234", locationFields),
		compatBatchItem("Docked", ts, "F1234", dockedFields),
	}
	body := compatBatchBody(t, items)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "mixed batch should be accepted; body: %s", rr.Body.String())

	var resp ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 3, resp.EventsWritten)

	require.Len(t, repo.insertCalls, 1)
	assert.Len(t, repo.insertCalls[0].events, 3)
}

func TestIngestCompat_Batch_OneUnknown_RejectsAll(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	ts := validTimestamp()
	items := []map[string]any{
		compatBatchItem("FSDJump", ts, "F1234", fsdJumpFields),
		compatBatchItem("UnknownHackedEvent999", ts, "F1234", map[string]any{}),
		compatBatchItem("Docked", ts, "F1234", dockedFields),
	}
	body := compatBatchBody(t, items)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown event type")
	assert.Len(t, repo.insertCalls, 0, "entire batch must be rejected when any event has an unknown type")
}

// ─── Timestamp format tests ───────────────────────────────────────────────────

func TestIngestCompat_TimestampFormats(t *testing.T) {
	// Use a recent base time so timestamps are within the 365-day window.
	base := validTimestamp() // e.g. "2026-04-15T09:00:00Z"

	// RFC3339 with milliseconds: insert sub-second component.
	// base is already RFC3339 without sub-seconds; append .000 before Z.
	baseWithMs := base[:len(base)-1] + ".000Z" // "2026-04-15T09:00:00.000Z"

	// ED journal bare UTC is the same as RFC3339 without sub-seconds for UTC.
	timestamps := []struct {
		name string
		ts   string
	}{
		// Standard RFC3339 (Go time.RFC3339) — the common ED client format
		{"RFC3339 no subseconds", base},
		// RFC3339 with milliseconds (time.RFC3339Nano) — some clients emit this
		{"RFC3339 with milliseconds", baseWithMs},
		// ED journal bare UTC format — identical to RFC3339/Z for whole seconds
		{"ED journal bare UTC", base},
	}

	for _, tc := range timestamps {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockCommanderRepo{}
			repo.insertResult.inserted = 1
			srv := newIngestTestServer(t, repo)

			body := compatSingleBody(t, "FSDJump", tc.ts, "F1234", fsdJumpFields)
			req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
			rr := httptest.NewRecorder()
			srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code, "timestamp %q should be accepted; body: %s", tc.ts, rr.Body.String())
		})
	}
}

// ─── Missing-field rejection tests ───────────────────────────────────────────

func TestIngestCompat_MissingTimestamp_Rejected(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	// Build payload without a timestamp — mirrors a bug in the Flutter client.
	b, _ := json.Marshal(map[string]any{
		"event": map[string]any{
			// "timestamp" intentionally omitted
			"event":          "FSDJump",
			"fid":            "F1234",
			"commander_name": "Test CMDR",
			"event_data":     fsdJumpFields,
		},
	})
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", string(b), "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	// An empty timestamp fails the RFC3339 parse — handler returns 400.
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Len(t, repo.insertCalls, 0)
}

func TestIngestCompat_MissingEventType_Rejected(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	// Build payload without an event type — mirrors a bug in the Flutter client.
	b, _ := json.Marshal(map[string]any{
		"event": map[string]any{
			"timestamp":      validTimestamp(),
			// "event" intentionally omitted
			"fid":            "F1234",
			"commander_name": "Test CMDR",
			"event_data":     fsdJumpFields,
		},
	})
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", string(b), "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	// Empty event type is not in the allowlist — handler returns 400.
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown event type")
	assert.Len(t, repo.insertCalls, 0)
}

// ─── Extra fields ─────────────────────────────────────────────────────────────

func TestIngestCompat_ExtraFields_Accepted(t *testing.T) {
	// The Flutter client passes the full raw ED journal event as event_data.
	// These extra fields should be stored verbatim without error.
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	richEventData := map[string]any{
		// All fields from a real FSDJump journal line
		"StarSystem":            "Shinrarta Dezhra",
		"SystemAddress":         float64(3932277478106),
		"StarPos":               []any{-55.21875, 65.1875, -4.15625},
		"Body":                  "Shinrarta Dezhra",
		"BodyID":                float64(0),
		"BodyType":              "Star",
		"JumpDist":              14.852,
		"FuelUsed":              1.234,
		"FuelLevel":             28.766,
		"SystemAllegiance":      "Independent",
		"SystemEconomy":         "$economy_HighTech;",
		"SystemSecondEconomy":   "$economy_Military;",
		"SystemGovernment":      "$government_Democracy;",
		"SystemSecurity":        "$SYSTEM_SECURITY_high;",
		"Population":            float64(85206935),
		"ThargoidWar":           nil,
		"Factions": []any{
			map[string]any{
				"Name":            "Pilots Federation Local Branch",
				"FactionState":    "None",
				"Government":      "Democracy",
				"Influence":       0.039,
				"Allegiance":      "PilotsFederation",
				"Happiness":       "$Faction_HappinessBand2;",
				"MyReputation":    100.0,
			},
		},
		"SystemFaction": map[string]any{
			"Name": "Pilots Federation Local Branch",
		},
		"Conflicts": []any{},
		"PowerplayState": "Contested",
		"Wanted":          false,
	}

	body := compatSingleBody(t, "FSDJump", validTimestamp(), "F1234", richEventData)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code,
		"payload with many extra ED fields should be accepted verbatim; body: %s", rr.Body.String())
	assert.Equal(t, 1, resp(t, rr).EventsWritten)
	assert.Len(t, repo.insertCalls, 1)
}

// resp decodes the ingestResponse from a recorder for assertion.
func resp(t *testing.T, rr *httptest.ResponseRecorder) ingestResponse {
	t.Helper()
	var r ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&r))
	return r
}
