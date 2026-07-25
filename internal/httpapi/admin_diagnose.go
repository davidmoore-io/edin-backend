package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// allowedDiagnoseChecks is the explicit allowlist. ANY value in a
// /admin/diagnose request body that is not a key here is rejected with 400
// BEFORE any probe runs. Its container values must be a subset of the
// docker-inspect-sidecar allowlist. See cmd/docker-inspect-sidecar/ALLOWLIST.md.
var allowedDiagnoseChecks = map[string]struct {
	container string // sidecar container to inspect
	probe     func(ctx context.Context, deps diagnoseDeps) probeResult
}{
	"galaxy-reader": {
		container: "eddn-timescaledb",
		probe:     func(ctx context.Context, d diagnoseDeps) probeResult { return probeGalaxyReader(ctx, d.galaxyReader) },
	},
	"edin-timescaledb": {
		container: "edin-timescaledb",
		probe:     func(ctx context.Context, d diagnoseDeps) probeResult { return probePostgres(ctx, d.edinTS) },
	},
	"eddn-timescaledb": {
		container: "eddn-timescaledb",
		probe:     func(ctx context.Context, d diagnoseDeps) probeResult { return probePostgres(ctx, d.eddnTS) },
	},
	"eddn-listener": {
		container: "eddn-listener",
		probe:     func(ctx context.Context, d diagnoseDeps) probeResult { return probeEDDNListener(ctx, d.listener) },
	},
}

type diagnoseDeps struct {
	galaxyReader galaxyReaderProber
	edinTS       pgProber
	eddnTS       pgProber
	listener     listenerLagProber
	sidecar      sidecarInspector
}

type sidecarInspector interface {
	Inspect(ctx context.Context, name string) (containerState, error)
}

type diagnoseRequest struct {
	Checks []string `json:"checks"`
}

type diagnoseResponse struct {
	CheckedAt time.Time              `json:"checked_at"`
	Results   map[string]probeResult `json:"results"`
}

func diagnoseHandler(deps diagnoseDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req diagnoseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}

		// Allowlist enforcement BEFORE any probe runs. Per spec §5
		// 'Mandatory implementation requirements (1)'.
		for _, name := range req.Checks {
			if _, ok := allowedDiagnoseChecks[name]; !ok {
				http.Error(w, "unknown check: "+name, http.StatusBadRequest)
				return
			}
		}

		results := make(map[string]probeResult, len(req.Checks))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, name := range req.Checks {
			name := name
			spec := allowedDiagnoseChecks[name]
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()

				res := spec.probe(ctx, deps)

				// Best-effort container-state lookup via sidecar; fail-open if
				// the sidecar is unreachable (per spec §5 (4)).
				state, err := deps.sidecar.Inspect(ctx, spec.container)
				if err == nil {
					res.ContainerState = &state
				} else if errors.Is(err, errUnknownContainer) {
					// Allowlist mismatch between sidecar and diagnose. Bug.
					// Surface it without crashing the whole report.
					res.Error = "sidecar 404 (allowlist mismatch): " + spec.container
				}
				// errSidecarUnreachable / errSidecarFailed: container_status
				// stays nil; the underlying probe result is preserved.

				mu.Lock()
				results[name] = res
				mu.Unlock()
			}()
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(diagnoseResponse{
			CheckedAt: time.Now().UTC(),
			Results:   results,
		})
	})
}
