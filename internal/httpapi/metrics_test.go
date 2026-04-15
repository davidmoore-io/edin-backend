package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIngestMetrics_AcceptedCounter verifies that edin_ingest_events_total{status="accepted"}
// increments after a successful single-event ingest call.
func TestIngestMetrics_AcceptedCounter(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	m := initEdinMetrics()

	fid := "F-METRICS-ACCEPT"
	fh := fidHash(fid)

	// Capture baseline (may be non-zero if tests run in parallel or previous test ran first).
	baseline := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("accepted", fh))

	body := singleEventBody("FSDJump", validTimestamp(), fid)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, fid, "Metrics CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	after := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("accepted", fh))
	assert.Equal(t, baseline+1, after, "edin_ingest_events_total{status=accepted} should have incremented by 1")
}

// TestIngestMetrics_RejectedCounter verifies that edin_ingest_events_total{status="rejected"}
// increments when an event with an empty type is submitted.
func TestIngestMetrics_RejectedCounter(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	m := initEdinMetrics()

	fid := "F-METRICS-REJECT"
	fh := fidHash(fid)

	baseline := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("rejected", fh))

	// Submit an event with an empty type — handler should reject it.
	body := singleEventBody("", validTimestamp(), fid)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, fid, "Metrics CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)

	after := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("rejected", fh))
	assert.Equal(t, baseline+1, after, "edin_ingest_events_total{status=rejected} should have incremented by 1")
}

// TestIngestMetrics_BatchAcceptedCounter verifies that edin_ingest_events_total{status="accepted"}
// increments by the number of inserted events in a batch ingest.
func TestIngestMetrics_BatchAcceptedCounter(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 3
	srv := newIngestTestServer(t, repo)

	m := initEdinMetrics()

	fid := "F-METRICS-BATCH"
	fh := fidHash(fid)

	baseline := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("accepted", fh))

	body := batchEventsBody("FSDJump", validTimestamp(), 3)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, fid, "Metrics CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	after := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("accepted", fh))
	assert.Equal(t, baseline+3, after, "edin_ingest_events_total{status=accepted} should have incremented by 3")
}

// TestIngestMetrics_DuplicateCounter verifies that edin_ingest_events_total{status="duplicate"}
// increments for events the repository reports as duplicates.
func TestIngestMetrics_DuplicateCounter(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	repo.insertResult.dups = 2
	srv := newIngestTestServer(t, repo)

	m := initEdinMetrics()

	fid := "F-METRICS-DUP"
	fh := fidHash(fid)

	baselineDup := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("duplicate", fh))

	body := batchEventsBody("FSDJump", validTimestamp(), 3)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, fid, "Metrics CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	afterDup := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("duplicate", fh))
	assert.Equal(t, baselineDup+2, afterDup, "edin_ingest_events_total{status=duplicate} should have incremented by 2")
}

// TestIngestMetrics_BatchRejectedCounter verifies that batch validation failures
// count all events in the batch as rejected.
func TestIngestMetrics_BatchRejectedCounter(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	m := initEdinMetrics()

	fid := "F-METRICS-BATCHREJ"
	fh := fidHash(fid)

	baseline := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("rejected", fh))

	// 3 events, one has an empty type — entire batch rejected.
	ts := validTimestamp()
	events := []map[string]any{
		{"timestamp": ts, "event": "FSDJump", "fid": fid, "commander_name": "Test", "event_data": map[string]any{}},
		{"timestamp": ts, "event": "", "fid": fid, "commander_name": "Test", "event_data": map[string]any{}},
		{"timestamp": ts, "event": "Docked", "fid": fid, "commander_name": "Test", "event_data": map[string]any{}},
	}
	b, err := json.Marshal(map[string]any{"events": events})
	require.NoError(t, err)

	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", string(b), fid, "Metrics CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)

	after := testutil.ToFloat64(m.ingestEventsTotal.WithLabelValues("rejected", fh))
	// All 3 events in the batch should be counted as rejected.
	assert.Equal(t, baseline+3, after, "all events in a rejected batch should be counted")
}

// TestFidHash verifies the FID hashing function produces consistent 8-char hex output.
func TestFidHash(t *testing.T) {
	h := fidHash("F12345")
	assert.Len(t, h, 8, "fidHash should return exactly 8 hex characters")

	// Same input must always produce the same output.
	assert.Equal(t, h, fidHash("F12345"), "fidHash must be deterministic")

	// Different inputs must produce different hashes (overwhelmingly likely for these values).
	assert.NotEqual(t, fidHash("F12345"), fidHash("F99999"), "different FIDs should produce different hashes")
}
