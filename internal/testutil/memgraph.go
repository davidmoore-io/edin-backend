//go:build integration || integration_search

// Package testutil — Memgraph testcontainers harness.
//
// This file mirrors the TimescaleDB harness in db.go. It exists so that
// memgraph-package integration tests can run hermetically in any environment
// with Docker/Podman, without relying on an external instance reachable via
// MEMGRAPH_TEST_HOST.
//
// The image is pinned to memgraph/memgraph:3.8.1 to match the version verified
// in production. Tantivy tokenizer behaviour can shift between minor versions,
// so we want test parity with what runs live.
//
// The init Cypher is rendered from the production Ansible Jinja2 template
// (single source of truth). Keeping that linkage means the schema we test
// against is the schema we deploy.
package testutil

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/edin-space/edin-backend/internal/memgraph"
)

const (
	testMemgraphImage   = "memgraph/memgraph:3.10.1"
	testMemgraphBoltPrt = "7687/tcp"
	testStartupTimeout  = 60 * time.Second
)

// StartTestMemgraph spins up an ephemeral Memgraph 3.8.1 container, applies
// the production schema (indexes, constraints, seed Power nodes, including
// the text indexes added by this feature), and returns a *memgraph.Client
// pointing at the container. Cleanup is registered with t.Cleanup.
//
// The harness is hermetic: no external services, no env vars, no host paths.
// All it requires is a working Docker/Podman socket.
func StartTestMemgraph(t *testing.T) *memgraph.Client {
	t.Helper()

	// Ryuk (the testcontainers reaper) cannot start under rootless Podman
	// because it expects /var/run/docker.sock. The rest of this repo follows
	// the same workaround in TestMain (see internal/store/commander_migrate_test.go);
	// we set it here so every caller of StartTestMemgraph inherits the fix.
	// Containers are still cleaned up via t.Cleanup → ctr.Terminate below.
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        testMemgraphImage,
		ExposedPorts: []string{testMemgraphBoltPrt},
		// Disable SSL on the Bolt listener; client connects with NoAuth.
		Cmd: []string{
			"--also-log-to-stderr=true",
			"--log-level=WARNING",
		},
		WaitingFor: wait.ForLog("You are running Memgraph").WithStartupTimeout(testStartupTimeout),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start memgraph container: %v", err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, testMemgraphBoltPrt)
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("mapped bolt port: %v", err)
	}

	client, err := memgraph.NewClient(memgraph.Config{
		Host: host,
		Port: port.Int(),
	})
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("new memgraph client: %v", err)
	}

	// Memgraph accepts Bolt connections slightly after the startup log line.
	// Poll Connect until it succeeds or the deadline fires.
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := waitForBolt(connectCtx, client); err != nil {
		_ = client.Close(ctx)
		_ = ctr.Terminate(ctx)
		t.Fatalf("memgraph bolt never became ready: %v", err)
	}

	if err := applyInitSchema(ctx, client); err != nil {
		_ = client.Close(ctx)
		_ = ctr.Terminate(ctx)
		t.Fatalf("apply init schema: %v", err)
	}

	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			_ = client.Close(ctx)
			termCtx, termCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer termCancel()
			if err := ctr.Terminate(termCtx); err != nil {
				t.Logf("warning: failed to terminate memgraph container: %v", err)
			}
		})
	})

	return client
}

// waitForBolt polls Client.Connect until it succeeds or the context expires.
// The wait.ForLog strategy fires before the Bolt listener is fully ready,
// so we need a follow-up TCP/Bolt-level readiness check.
func waitForBolt(ctx context.Context, c *memgraph.Client) error {
	deadline, _ := ctx.Deadline()
	for {
		if err := c.Connect(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bolt did not become ready before deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// applyInitSchema reads the production init template, strips the
// auth-conditional block (we run unauthenticated in tests), and executes
// each Cypher statement against the client. Mirrors what production Ansible
// applies — keeping a single source of truth for schema.
func applyInitSchema(ctx context.Context, c *memgraph.Client) error {
	stmts, err := loadInitStatements()
	if err != nil {
		return fmt.Errorf("load init statements: %w", err)
	}

	session := c.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	for _, stmt := range stmts {
		if _, err := session.Run(ctx, stmt, nil); err != nil {
			return fmt.Errorf("init stmt %q: %w", truncate(stmt, 80), err)
		}
	}
	return nil
}

// loadInitStatements reads the j2 template from the sibling edin-data repo,
// strips the {% if memgraph_auth_enabled %} ... {% endif %} block, and returns
// each non-empty, non-comment line as a separate statement.
func loadInitStatements() ([]string, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	// internal/testutil/memgraph.go → ../../.. = edin-backend → ../edin-data/...
	templatePath := filepath.Join(
		filepath.Dir(thisFile),
		"..", "..", "..", "edin-data",
		"ansible", "roles", "databases", "templates",
		"memgraph-init.cypher.j2",
	)
	f, err := os.Open(templatePath)
	if err != nil {
		return nil, fmt.Errorf("open template at %s: %w", templatePath, err)
	}
	defer f.Close()

	var stmts []string
	skipping := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Strip the Jinja auth conditional. Local/test runs don't authenticate.
		if strings.HasPrefix(trimmed, "{% if memgraph_auth_enabled") {
			skipping = true
			continue
		}
		if trimmed == "{% endif %}" {
			skipping = false
			continue
		}
		if skipping {
			continue
		}

		// Skip blank lines and Cypher comments.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Each statement is on a single line in this template (Memgraph init parser
		// requirement, documented at the top of the j2 file). Trailing semicolon
		// is part of the statement; keep it.
		stmts = append(stmts, trimmed)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan template: %w", err)
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("no statements parsed from template at %s", templatePath)
	}
	return stmts, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
