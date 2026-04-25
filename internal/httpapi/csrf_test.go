package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edin-space/edin-backend/internal/observability"
)

func newCSRFTestServer() *Server {
	return &Server{logger: observability.NewLogger("test")}
}

// TestRequireFetchHeader_Present_Passes verifies the helper accepts the
// header value "1" and returns true without writing to the response.
func TestRequireFetchHeader_Present_Passes(t *testing.T) {
	s := newCSRFTestServer()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	rr := httptest.NewRecorder()

	if !s.requireFetchHeader(rr, req) {
		t.Fatal("expected requireFetchHeader to return true with X-Edin-Fetch: 1")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("did not expect any status to be written, got %d", rr.Code)
	}
}

// TestRequireFetchHeader_Missing_Returns400 verifies the helper writes a
// 400 response when the header is absent and returns false.
func TestRequireFetchHeader_Missing_Returns400(t *testing.T) {
	s := newCSRFTestServer()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rr := httptest.NewRecorder()

	if s.requireFetchHeader(rr, req) {
		t.Fatal("expected requireFetchHeader to return false when header is missing")
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "X-Edin-Fetch") {
		t.Errorf("expected error body to mention X-Edin-Fetch, got: %s", rr.Body.String())
	}
}

// TestRequireFetchHeader_WrongValue_Returns400 verifies that any value
// other than the literal "1" is rejected — there is no leniency.
func TestRequireFetchHeader_WrongValue_Returns400(t *testing.T) {
	for _, v := range []string{"0", "true", "yes", " 1", "1 "} {
		t.Run("value="+v, func(t *testing.T) {
			s := newCSRFTestServer()
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			req.Header.Set("X-Edin-Fetch", v)
			rr := httptest.NewRecorder()

			if s.requireFetchHeader(rr, req) {
				t.Fatalf("expected requireFetchHeader to return false for value %q", v)
			}
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for value %q", rr.Code, v)
			}
		})
	}
}
