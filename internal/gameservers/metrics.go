// Package gameservers provides Prometheus metrics and monitoring for SSG game servers.
package gameservers

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Prometheus collectors for game server monitoring.
type Metrics struct {
	// ServerUp indicates if the server is online (1) or offline (0)
	ServerUp *prometheus.GaugeVec

	// Players is the current player count
	Players *prometheus.GaugeVec

	// MaxPlayers is the maximum player capacity
	MaxPlayers *prometheus.GaugeVec

	// LastCheck is the Unix timestamp of the last successful query
	LastCheck *prometheus.GaugeVec

	// QueryDuration is the time taken to query each server
	QueryDuration *prometheus.GaugeVec

	// QueryErrors counts failed queries per server
	QueryErrors *prometheus.CounterVec
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
)

// InitMetrics registers Prometheus collectors for game server monitoring (idempotent).
func InitMetrics(namespace string) *Metrics {
	metricsOnce.Do(func() {
		metricsInst = &Metrics{
			ServerUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "up",
				Help:      "Whether the game server is online (1) or offline (0).",
			}, []string{"server", "type"}),

			Players: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "players",
				Help:      "Current number of players on the server.",
			}, []string{"server", "type"}),

			MaxPlayers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "max_players",
				Help:      "Maximum player capacity of the server.",
			}, []string{"server", "type"}),

			LastCheck: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "last_check_timestamp",
				Help:      "Unix timestamp of the last status check.",
			}, []string{"server", "type"}),

			QueryDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "query_duration_seconds",
				Help:      "Time taken to query the server status.",
			}, []string{"server", "type"}),

			QueryErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "gameserver",
				Name:      "query_errors_total",
				Help:      "Total number of failed status queries.",
			}, []string{"server", "type", "error_type"}),
		}

		prometheus.MustRegister(
			metricsInst.ServerUp,
			metricsInst.Players,
			metricsInst.MaxPlayers,
			metricsInst.LastCheck,
			metricsInst.QueryDuration,
			metricsInst.QueryErrors,
		)
	})
	return metricsInst
}

// GetMetrics returns the singleton metrics instance.
// Returns nil if InitMetrics hasn't been called.
func GetMetrics() *Metrics {
	return metricsInst
}
