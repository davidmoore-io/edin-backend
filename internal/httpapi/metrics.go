package httpapi

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// edinMetrics holds Prometheus metrics for the Copilot and Commander endpoints.
// It is registered once via initEdinMetrics and then accessed via package-level vars.
type edinMetrics struct {
	ingestEventsTotal    *prometheus.CounterVec
	ingestLatencySeconds *prometheus.HistogramVec

	commanderAuthAttemptsTotal *prometheus.CounterVec

	copilotChatSessionsActive prometheus.Gauge
	copilotToolCallsTotal     *prometheus.CounterVec
}

var (
	emetOnce sync.Once
	emet     *edinMetrics
)

// initEdinMetrics registers all Copilot/Commander Prometheus metrics (idempotent).
// Subsequent calls return the same instance without re-registering.
func initEdinMetrics() *edinMetrics {
	emetOnce.Do(func() {
		ingestEventsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edin_ingest_events_total",
			Help: "Total number of journal events processed by the ingest endpoints, labelled by outcome and FID hash.",
		}, []string{"status", "fid_hash"})

		ingestLatencySeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "edin_ingest_latency_seconds",
			Help:    "End-to-end latency of ingest handler calls.",
			Buckets: prometheus.DefBuckets,
		}, []string{"endpoint"})

		commanderAuthAttemptsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edin_commander_auth_attempts_total",
			Help: "Total number of commander auth callback outcomes.",
		}, []string{"outcome"})

		copilotChatSessionsActive := prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "edin_copilot_chat_sessions_active",
			Help: "Number of currently active Copilot WebSocket sessions.",
		})

		copilotToolCallsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "edin_copilot_tool_calls_total",
			Help: "Total number of tool calls made during Copilot chat sessions.",
		}, []string{"tool_name"})

		prometheus.MustRegister(
			ingestEventsTotal,
			ingestLatencySeconds,
			commanderAuthAttemptsTotal,
			copilotChatSessionsActive,
			copilotToolCallsTotal,
		)

		emet = &edinMetrics{
			ingestEventsTotal:          ingestEventsTotal,
			ingestLatencySeconds:       ingestLatencySeconds,
			commanderAuthAttemptsTotal: commanderAuthAttemptsTotal,
			copilotChatSessionsActive:  copilotChatSessionsActive,
			copilotToolCallsTotal:      copilotToolCallsTotal,
		}
	})
	return emet
}

// fidHash returns the first 8 hex characters of the SHA-256 hash of the given FID.
// This provides an opaque, privacy-preserving label for Prometheus metrics.
func fidHash(fid string) string {
	return fmt.Sprintf("%.8s", fmt.Sprintf("%x", sha256.Sum256([]byte(fid))))
}
