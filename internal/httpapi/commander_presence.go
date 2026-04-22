package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/security"
)

// Presence design — "is the commander's desktop client actively connected?"
//
// Two endpoints:
//
//   POST /api/v1/commander/heartbeat   (desktop Flutter, Bearer auth)
//   GET  /api/commander/presence       (web frontend, httpOnly cookie auth)
//
// Redis schema:
//   key:   edin:presence:<fid>
//   value: JSON {"last_seen": RFC3339, "client_version": "..."}
//   ttl:   presenceTTL (60s). When the client stops heartbeating for 60s the
//          key expires and presence reads return is_live=false.
//
// Isolation model:
//   * FID is taken *only* from auth.ClaimsFromContext (set by withCommanderAuth).
//     Nothing in the request body or URL can override it.
//   * The Redis key embeds the FID, so commander A's writes cannot touch
//     commander B's key and commander B's reads cannot see commander A's state.
//   * Request body carries client_version only — advisory metadata for the
//     tooltip display. It is never trusted for identity.
//   * Per-FID rate limit (heartbeatRateLimit / heartbeatRateWindow) bounds
//     write volume even if a client goes rogue.
//
// Not intentionally supported:
//   * Cross-commander presence queries. There is no admin "who's online"
//     endpoint, and this handler cannot be coerced into one. Future admin-
//     scoped views must be a separate handler with its own role check.

const (
	presenceKeyPrefix    = "edin:presence:"
	presenceTTL          = 60 * time.Second
	heartbeatRateLimit   = 6                // 6 writes per minute per FID
	heartbeatRateWindow  = time.Minute      // headroom over the 30s client interval
	maxClientVersionLen  = 64               // cap advisory free-text field length
)

// presenceRecord is stored in Redis as JSON at key edin:presence:<fid>.
type presenceRecord struct {
	LastSeen      time.Time `json:"last_seen"`
	ClientVersion string    `json:"client_version,omitempty"`
}

// heartbeatFIDRateLimiter bounds write volume to Redis per FID. Separate from
// the ingest rate limiter because the two have different operating regimes and
// we want either one to be tunable without affecting the other.
type heartbeatFIDRateLimiter struct {
	buckets sync.Map
}

func newHeartbeatFIDRateLimiter() *heartbeatFIDRateLimiter {
	return &heartbeatFIDRateLimiter{}
}

// Allow returns true if the FID has heartbeat-budget remaining.
func (r *heartbeatFIDRateLimiter) Allow(fid string) bool {
	bucket, _ := r.buckets.LoadOrStore(fid,
		security.NewTokenBucket(heartbeatRateLimit, heartbeatRateWindow))
	return bucket.(*security.TokenBucket).Allow()
}

// presenceKey returns the Redis key for the given FID. Separate helper so the
// key layout is visible in exactly one place — if we ever need to namespace it
// differently (per-env, per-cluster) this is the only site to change.
func presenceKey(fid string) string {
	return presenceKeyPrefix + fid
}

// ─── heartbeat ────────────────────────────────────────────────────────────────

// heartbeatRequest is the optional request body. The commander is identified
// by the validated JWT, never by any body field, so the only thing the body
// carries is advisory display metadata.
type heartbeatRequest struct {
	ClientVersion string `json:"client_version,omitempty"`
}

// handleCommanderHeartbeat handles POST /api/v1/commander/heartbeat.
//
// Mounted behind withCommanderAuth. Writes a short-lived Redis key keyed on
// the FID from the validated JWT.
func (s *Server) handleCommanderHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.redisClient == nil {
		// Presence requires Redis — fail closed but with a dedicated status so
		// the client can degrade rather than appearing to succeed.
		s.writeError(w, http.StatusServiceUnavailable, "presence service unavailable")
		return
	}

	if !s.heartbeatRateLimiter.Allow(claims.FID) {
		s.writeError(w, http.StatusTooManyRequests, "heartbeat rate limit exceeded")
		return
	}

	// Body is optional. A client_version longer than maxClientVersionLen is
	// almost certainly junk / an attempt to store large text via an advisory
	// field, so truncate rather than round-tripping it.
	var body heartbeatRequest
	if r.Body != nil && r.ContentLength != 0 {
		// Ignore decode errors — body is entirely optional, and a malformed
		// body shouldn't prevent the heartbeat from taking effect.
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	clientVersion := body.ClientVersion
	if len(clientVersion) > maxClientVersionLen {
		clientVersion = clientVersion[:maxClientVersionLen]
	}

	record := presenceRecord{
		LastSeen:      time.Now().UTC(),
		ClientVersion: clientVersion,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_heartbeat: marshal fid=%s", claims.FID), err)
		s.writeError(w, http.StatusInternalServerError, "failed to record heartbeat")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.redisClient.Set(ctx, presenceKey(claims.FID), payload, presenceTTL).Err(); err != nil {
		s.logger.Error(fmt.Sprintf("commander_heartbeat: redis set fid=%s", claims.FID), err)
		s.writeError(w, http.StatusServiceUnavailable, "failed to record heartbeat")
		return
	}

	// No body to return — presence is read via GET /api/commander/presence.
	w.WriteHeader(http.StatusNoContent)
}

// ─── presence read ────────────────────────────────────────────────────────────

// presenceResponse is returned by GET /api/commander/presence.
type presenceResponse struct {
	IsLive        bool       `json:"is_live"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	ClientVersion string     `json:"client_version,omitempty"`
}

// handleCommanderPresence handles GET /api/commander/presence.
//
// Reads the Redis presence key for the commander identified by the JWT and
// returns is_live plus (if present) last_seen and client_version for tooltip
// display. Expired key → is_live=false with no last_seen. There is deliberately
// no way for a caller to request another commander's presence.
func (s *Server) handleCommanderPresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.redisClient == nil {
		// Without Redis presence is indeterminate. Returning is_live=false is
		// the safer default — UI reads "not connected" rather than claiming a
		// stale green state.
		s.writeJSON(w, http.StatusOK, presenceResponse{IsLive: false})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	raw, err := s.redisClient.Get(ctx, presenceKey(claims.FID)).Bytes()
	if errors.Is(err, redis.Nil) {
		s.writeJSON(w, http.StatusOK, presenceResponse{IsLive: false})
		return
	}
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_presence: redis get fid=%s", claims.FID), err)
		s.writeError(w, http.StatusServiceUnavailable, "failed to read presence")
		return
	}

	var record presenceRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		// Corrupt record in Redis — treat as absent rather than 500ing the UI.
		s.logger.Warn(fmt.Sprintf("commander_presence: corrupt record fid=%s: %v", claims.FID, err))
		s.writeJSON(w, http.StatusOK, presenceResponse{IsLive: false})
		return
	}

	s.writeJSON(w, http.StatusOK, presenceResponse{
		IsLive:        true,
		LastSeen:      &record.LastSeen,
		ClientVersion: record.ClientVersion,
	})
}
