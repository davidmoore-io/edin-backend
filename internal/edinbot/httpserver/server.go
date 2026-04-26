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

// PollTrigger is satisfied by the scheduler. Lets the debug endpoint kick a
// binding's poll cycle without waiting for the next interval. Internal-only:
// the bot's :8080 listener is never published outside the docker network.
type PollTrigger interface {
	TriggerNow(bindingID string) error
	BindingIDs() []string
}

type Config struct {
	Addr    string
	Health  HealthOracle
	Trigger PollTrigger
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

	// /debug/poll/{binding_id} — POST runs an immediate tick for that binding.
	// GET lists the known binding IDs. Internal-only (docker-network reachable
	// only); we still accept POST-without-body so a plain `curl -XPOST` works.
	mux.HandleFunc("/debug/poll/", func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Trigger == nil {
			http.Error(w, "trigger not configured", http.StatusServiceUnavailable)
			return
		}
		bindingID := r.URL.Path[len("/debug/poll/"):]
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"bindings": s.cfg.Trigger.BindingIDs(),
			})
		case http.MethodPost:
			if bindingID == "" {
				http.Error(w, "binding id required: POST /debug/poll/{binding_id}", http.StatusBadRequest)
				return
			}
			if err := s.cfg.Trigger.TriggerNow(bindingID); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"triggered": bindingID,
				"note":      "tick queued; check logs and discord.poll_cycles for outcome",
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
