// docker-inspect-sidecar — a tiny security-isolated service that holds the
// docker socket on behalf of the control-API. Single endpoint:
//
//	GET /inspect/{name}    where {name} ∈ allowedContainers()
//
// Returns 200 + JSON {status, health} for allowed names; 404 for everything
// else; 502 if the docker daemon is unreachable.
//
// SECURITY: this binary MUST NOT import os/exec. All Docker interaction goes
// through the official Docker Engine API SDK over the unix socket. The
// allowlist below is the only source of truth — adding to it requires a plan
// task, not a config change. See ALLOWLIST.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	docker "github.com/docker/docker/client"
)

// containerState is the trimmed shape we expose. Source: docker inspect's
// `.State` block.
type containerState struct {
	Status string `json:"status"`           // "running" | "exited" | "paused" | ...
	Health string `json:"health,omitempty"` // "healthy" | "unhealthy" | "starting" | "" if no healthcheck
}

// dockerClient is the abstraction over the Docker SDK so tests can inject fakes.
type dockerClient interface {
	Inspect(ctx context.Context, name string) (containerState, error)
}

// realDockerClient wraps github.com/docker/docker/client.
type realDockerClient struct{ cli *docker.Client }

func newRealDockerClient() (dockerClient, error) {
	cli, err := docker.NewClientWithOpts(docker.FromEnv, docker.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client init: %w", err)
	}
	return &realDockerClient{cli: cli}, nil
}

func (r *realDockerClient) Inspect(ctx context.Context, name string) (containerState, error) {
	info, err := r.cli.ContainerInspect(ctx, name)
	if err != nil {
		return containerState{}, err
	}
	out := containerState{Status: info.State.Status}
	if info.State.Health != nil {
		out.Health = info.State.Health.Status
	}
	return out, nil
}

// allowedContainers is the single source of truth. Lock-step with
// /admin/diagnose's allowlist in internal/httpapi/admin_diagnose.go. Adding to
// either without the other is a bug; the test in TestSidecar_AllowlistContents
// guards this. See ALLOWLIST.md.
func allowedContainers() []string {
	return []string{
		"memgraph",
		"edin-timescaledb",
		"eddn-timescaledb",
		"eddn-listener",
	}
}

func isAllowed(name string) bool {
	for _, a := range allowedContainers() {
		if a == name {
			return true
		}
	}
	return false
}

// buildMux is exposed (lowercase package-private but accessible to main_test.go
// in the same package) so tests can mount the same handler tree the binary
// runs.
func buildMux(client dockerClient) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/inspect/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/inspect/")
		// Reject empty, multi-segment, or any name not on the allowlist.
		if name == "" || strings.Contains(name, "/") || !isAllowed(name) {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		state, err := client.Inspect(ctx, name)
		if err != nil {
			log.Printf("[ERROR] inspect %s: %v", name, err)
			http.Error(w, "docker inspect failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	})
	return mux
}

func main() {
	client, err := newRealDockerClient()
	if err != nil {
		log.Fatalf("[FATAL] docker client init: %v", err)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           buildMux(client),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("[INFO] docker-inspect-sidecar listening on %s; allowlist=%v", addr, allowedContainers())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[FATAL] listen: %v", err)
	}
}
