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

		if !s.rateLimiter.Allow(s.rateLimitKey(r)) {
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
