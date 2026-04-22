package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker to support WebSocket upgrades.
// It delegates to the underlying ResponseWriter's Hijack method.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("responseRecorder: underlying ResponseWriter does not implement http.Hijacker")
}

// Flush implements http.Flusher for streaming responses.
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) applyMiddlewares(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
		clientIP := clientIP(r)
		if requestID == "" {
			requestID = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, requestID)
		w.Header().Set("Vary", "Origin")

		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		r = r.WithContext(ctx)

		if r.Method == http.MethodOptions {
			s.applyCORSHeaders(w, r)
			w.WriteHeader(http.StatusNoContent)
			s.metrics.ObserveHTTP(r.Method, r.URL.Path, http.StatusNoContent, time.Since(start))
			return
		}

		// Skip the global limiter for endpoints that enforce their own per-FID
		// budget. The global bucket is keyed on the full Authorization header
		// and shared across every request from the same client; letting bulk-
		// ingest bursts drain it starved heartbeat (and every other commander
		// call) until it refilled, which is how the web presence indicator
		// kept flipping to "offline" mid-backfill. The per-FID limiters in the
		// respective handlers remain authoritative for those paths.
		if !isGloballyRateLimited(r.URL.Path) {
			// fall through — handler has its own limiter
		} else if !s.rateLimiter.Allow(s.rateLimitKey(r)) {
			s.logger.Warn(fmt.Sprintf("rate limit exceeded: %s %s request_id=%s client_ip=%s", r.Method, r.URL.Path, requestID, clientIP))
			s.metrics.ObserveHTTP(r.Method, r.URL.Path, http.StatusTooManyRequests, time.Since(start))
			s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		recorder := newResponseRecorder(w)
		s.applyCORSHeaders(recorder, r)
		next.ServeHTTP(recorder, r)

		duration := time.Since(start)
		s.metrics.ObserveHTTP(r.Method, r.URL.Path, recorder.status, duration)
		s.logger.Info(fmt.Sprintf("%s %s -> %d (%s) request_id=%s client_ip=%s", r.Method, r.URL.Path, recorder.status, duration, requestID, clientIP))
	})
}

// isGloballyRateLimited decides whether a request path should be gated by the
// blanket middleware rate limiter. Paths that run their own per-FID limiter
// inside the handler are exempt — the global layer would just create false
// 429s when a single client legitimately bursts above the blanket cap (e.g.
// during first-time journal backfill) without adding any incremental safety.
//
// Be explicit about exemptions: new endpoints default to "globally limited"
// unless deliberately added here. The list is intentionally small.
func isGloballyRateLimited(path string) bool {
	// Per-FID ingest limiter: commander_ingest.go — 100 req/min per FID.
	if path == "/api/v1/ingest/event" || path == "/api/v1/ingest/events" {
		return false
	}
	// Per-FID heartbeat limiter: commander_presence.go — 6 req/min per FID.
	if path == "/api/v1/commander/heartbeat" {
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if idx := strings.Index(xff, ","); idx >= 0 {
			xff = xff[:idx]
		}
		ip := strings.TrimSpace(xff)
		if ip != "" {
			return ip
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
