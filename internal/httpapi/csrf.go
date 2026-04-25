package httpapi

import "net/http"

// requireFetchHeader returns true if the X-Edin-Fetch: 1 header is present
// on the request. Otherwise it writes 400 Bad Request and returns false —
// the caller must return immediately when the result is false.
//
// Browsers do not attach custom headers like X-Edin-Fetch on cross-site
// form posts, so requiring it is a defence-in-depth complement to the
// SameSite=Lax cookie our admin auth uses. It is NOT a substitute for
// proper authentication — withKaineAuth + withKaineAdmin already enforce
// identity and authorisation; this header guards against a same-origin-but-
// off-app attacker tricking a logged-in admin into POSTing.
//
// 400 (not 403) is deliberate: the request is well-formed but missing
// required metadata; 400 communicates "your client didn't send what we
// need." The Task 8 admin API plan specifies 400; handleKaineToken's
// older inline check that returned 403 was refactored to use this helper
// and now also returns 400 for consistency.
func (s *Server) requireFetchHeader(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Edin-Fetch") != "1" {
		s.writeError(w, http.StatusBadRequest, "missing or invalid X-Edin-Fetch header")
		return false
	}
	return true
}
