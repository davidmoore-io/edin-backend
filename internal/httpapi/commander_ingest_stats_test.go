package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/stretchr/testify/assert"
)

func TestIngestStats_NoRepo_Returns503(t *testing.T) {
	// Server with commanderRepo: nil — ingest is not available.
	srv := &Server{
		cfg:    &config.Config{},
		logger: observability.NewLogger("test"),
		// commanderRepo deliberately nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ingest/stats", nil)
	rr := httptest.NewRecorder()
	srv.handleIngestStats(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), "ingest not available")
}

func TestIngestStats_MissingAuth_Returns401(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	// No Authorization header — middleware not applied, so context has no claims.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ingest/stats", nil)
	rr := httptest.NewRecorder()

	// Call through the middleware chain so the auth check runs.
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestStats))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
