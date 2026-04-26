// Package httpserver runs the edin-bot's internal :8080 HTTP server.
// Container-internal only — never published to the host, never behind Caddy.
// Exposes /healthz, /metrics, /version.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// HealthOracle is satisfied by the scheduler. Allows /healthz to make a
// per-binding judgement.
type HealthOracle interface {
	AllBindingsHealthy() bool
	PerBindingHealth() map[string]bool
}

type Config struct {
	Addr    string
	Health  HealthOracle
	Version string
}

type Server struct {
	cfg Config
}

func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	return &Server{cfg: cfg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Health == nil {
			http.Error(w, "no health oracle configured", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if s.cfg.Health.AllBindingsHealthy() {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "ok",
				"bindings": s.cfg.Health.PerBindingHealth(),
			})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "degraded",
			"bindings": s.cfg.Health.PerBindingHealth(),
		})
	})

	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, s.cfg.Version)
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintln(w, "# HELP edin_bot_up Whether the bot process is up.")
		fmt.Fprintln(w, "# TYPE edin_bot_up gauge")
		fmt.Fprintln(w, "edin_bot_up 1")
	})

	return mux
}

func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}
