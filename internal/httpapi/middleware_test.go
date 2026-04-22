package httpapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsGloballyRateLimited pins the exemption list for the blanket middleware
// rate limiter. New paths are globally limited by default — adding them to
// the exempt set must be a deliberate, tested change.
func TestIsGloballyRateLimited(t *testing.T) {
	t.Run("ingest single is exempt", func(t *testing.T) {
		assert.False(t, isGloballyRateLimited("/api/v1/ingest/event"),
			"ingest has its own per-FID limiter; global layer must not double-charge it")
	})
	t.Run("ingest batch is exempt", func(t *testing.T) {
		assert.False(t, isGloballyRateLimited("/api/v1/ingest/events"))
	})
	t.Run("heartbeat is exempt", func(t *testing.T) {
		assert.False(t, isGloballyRateLimited("/api/v1/commander/heartbeat"),
			"heartbeat shares the Bearer token with ingest; without exemption a backfill burst drains its budget and the web presence indicator falsely flips to offline")
	})

	// Everything else must still be limited. Spot-check a cross-section of
	// surfaces so a future refactor that loses a path isn't silently less
	// safe.
	for _, path := range []string{
		"/api/commander/auth/initiate",
		"/api/commander/auth/callback",
		"/api/commander/presence",
		"/api/v1/commander/events",
		"/api/v1/commander/location",
		"/api/v1/commander/profile",
		"/api/v1/ingest/stats",
		"/api/kaine/objectives",
		"/api/copilot/chat/ws",
		"/health",
	} {
		t.Run("default-limited: "+path, func(t *testing.T) {
			assert.True(t, isGloballyRateLimited(path),
				"path must stay on the global limiter unless it has its own per-FID cap")
		})
	}
}
