package memgraph

import (
	"context"
	"os"
	"testing"
	"time"
)

// skipIfNoMemgraph skips the test if Memgraph is not available.
// Set MEMGRAPH_TEST_HOST to run integration tests.
func skipIfNoMemgraph(t *testing.T) *Client {
	t.Helper()

	host := os.Getenv("MEMGRAPH_TEST_HOST")
	if host == "" {
		t.Skip("MEMGRAPH_TEST_HOST not set, skipping integration test")
	}

	cfg := Config{
		Host: host,
		Port: 7687,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Skipf("could not connect to Memgraph: %v", err)
	}

	return client
}

func TestGetSystemsInBounds(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test a small bounding box around Sol (0, 0, 0)
	req := GalaxyViewRequest{
		MinX:  -50,
		MaxX:  50,
		MinY:  -50,
		MaxY:  50,
		MinZ:  -50,
		MaxZ:  50,
		Limit: 1000,
	}

	systems, totalCount, err := client.GetSystemsInBounds(ctx, req)
	if err != nil {
		t.Fatalf("GetSystemsInBounds failed: %v", err)
	}

	if totalCount == 0 {
		t.Error("expected some systems near Sol, got 0")
	}

	if len(systems) == 0 {
		t.Error("expected systems in result, got empty slice")
	}

	// Verify systems are within bounds
	for _, sys := range systems {
		if sys.X < req.MinX || sys.X > req.MaxX {
			t.Errorf("system %s has X=%f outside bounds [%f, %f]", sys.Name, sys.X, req.MinX, req.MaxX)
		}
		if sys.Y < req.MinY || sys.Y > req.MaxY {
			t.Errorf("system %s has Y=%f outside bounds [%f, %f]", sys.Name, sys.Y, req.MinY, req.MaxY)
		}
		if sys.Z < req.MinZ || sys.Z > req.MaxZ {
			t.Errorf("system %s has Z=%f outside bounds [%f, %f]", sys.Name, sys.Z, req.MinZ, req.MaxZ)
		}
	}

	t.Logf("Found %d systems (total: %d) in 100ly cube around Sol", len(systems), totalCount)
}

func TestGetSystemsInBounds_WithFilters(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test with power filter
	req := GalaxyViewRequest{
		MinX:  -1000,
		MaxX:  1000,
		MinY:  -500,
		MaxY:  500,
		MinZ:  -1000,
		MaxZ:  1000,
		Limit: 100,
		Power: "Nakato Kaine",
	}

	systems, _, err := client.GetSystemsInBounds(ctx, req)
	if err != nil {
		t.Fatalf("GetSystemsInBounds with filter failed: %v", err)
	}

	// All returned systems should have the specified power
	for _, sys := range systems {
		if sys.ControllingPower != "Nakato Kaine" {
			t.Errorf("expected controlling_power='Nakato Kaine', got '%s' for system %s",
				sys.ControllingPower, sys.Name)
		}
	}

	t.Logf("Found %d Nakato Kaine systems", len(systems))
}

func TestGetSystemsInBounds_Truncation(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Request a large area with small limit
	req := GalaxyViewRequest{
		MinX:  -500,
		MaxX:  500,
		MinY:  -500,
		MaxY:  500,
		MinZ:  -500,
		MaxZ:  500,
		Limit: 10,
	}

	systems, totalCount, err := client.GetSystemsInBounds(ctx, req)
	if err != nil {
		t.Fatalf("GetSystemsInBounds failed: %v", err)
	}

	if len(systems) > req.Limit {
		t.Errorf("expected at most %d systems, got %d", req.Limit, len(systems))
	}

	if totalCount > len(systems) {
		t.Logf("Truncation working correctly: returned %d of %d total systems", len(systems), totalCount)
	}
}

func TestGetSystemDetail(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First, find Sol's ID
	solID, err := client.GetSystemIDByName(ctx, "Sol")
	if err != nil {
		t.Skipf("Sol not found in database: %v", err)
	}

	// Get full system detail
	detail, err := client.GetSystemDetail(ctx, solID)
	if err != nil {
		t.Fatalf("GetSystemDetail failed: %v", err)
	}

	if detail.System.Name != "Sol" {
		t.Errorf("expected system name 'Sol', got '%s'", detail.System.Name)
	}

	// Sol should have bodies
	if len(detail.Bodies) == 0 {
		t.Error("expected Sol to have bodies")
	}

	// Check for Earth
	foundEarth := false
	for _, body := range detail.Bodies {
		if body.Name == "Earth" {
			foundEarth = true
			if body.Type != "Planet" {
				t.Errorf("expected Earth type 'Planet', got '%s'", body.Type)
			}
			break
		}
	}

	if !foundEarth {
		t.Log("Earth not found in Sol's bodies (may be expected depending on data)")
	}

	t.Logf("Sol has %d bodies and %d stations", len(detail.Bodies), len(detail.Stations))
}

func TestGetSystemDetailByName(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	detail, err := client.GetSystemDetailByName(ctx, "Sol")
	if err != nil {
		t.Skipf("Sol not found: %v", err)
	}

	if detail.System.Name != "Sol" {
		t.Errorf("expected system name 'Sol', got '%s'", detail.System.Name)
	}

	// Verify coordinates are near origin
	if detail.System.X < -1 || detail.System.X > 1 {
		t.Errorf("Sol X coordinate should be near 0, got %f", detail.System.X)
	}
	if detail.System.Y < -1 || detail.System.Y > 1 {
		t.Errorf("Sol Y coordinate should be near 0, got %f", detail.System.Y)
	}
	if detail.System.Z < -1 || detail.System.Z > 1 {
		t.Errorf("Sol Z coordinate should be near 0, got %f", detail.System.Z)
	}
}

func TestGetSystemDetail_NotFound(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use an invalid ID
	_, err := client.GetSystemDetail(ctx, -999999999)
	if err == nil {
		t.Error("expected error for non-existent system")
	}

	if err != ErrSystemNotFound {
		t.Errorf("expected ErrSystemNotFound, got %v", err)
	}
}

func TestGetSystemDetailByName_NotFound(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.GetSystemDetailByName(ctx, "ThisSystemDoesNotExist12345")
	if err == nil {
		t.Error("expected error for non-existent system")
	}

	if err != ErrSystemNotFound {
		t.Errorf("expected ErrSystemNotFound, got %v", err)
	}
}

func TestGetSystemCoordinates(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First get Sol's ID
	solID, err := client.GetSystemIDByName(ctx, "Sol")
	if err != nil {
		t.Skipf("Sol not found: %v", err)
	}

	x, y, z, err := client.GetSystemCoordinates(ctx, solID)
	if err != nil {
		t.Fatalf("GetSystemCoordinates failed: %v", err)
	}

	// Sol should be at approximately (0, 0, 0)
	if x < -1 || x > 1 || y < -1 || y > 1 || z < -1 || z > 1 {
		t.Errorf("Sol coordinates should be near origin, got (%f, %f, %f)", x, y, z)
	}
}

func TestGetGalaxyViewStats(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stats, err := client.GetGalaxyViewStats(ctx)
	if err != nil {
		t.Fatalf("GetGalaxyViewStats failed: %v", err)
	}

	if stats.TotalSystems == 0 {
		t.Error("expected non-zero total systems")
	}

	if stats.TotalPowers == 0 {
		t.Error("expected non-zero total powers")
	}

	t.Logf("Galaxy stats: %d systems, %d population, %d powers, %d stations",
		stats.TotalSystems, stats.TotalPopulation, stats.TotalPowers, stats.TotalStations)
}

func TestSearchSystemsByPrefix(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Search for systems starting with "Sol"
	systems, err := client.SearchSystemsByPrefix(ctx, "Sol", 10)
	if err != nil {
		t.Fatalf("SearchSystemsByPrefix failed: %v", err)
	}

	if len(systems) == 0 {
		t.Error("expected some systems starting with 'Sol'")
	}

	// Verify all results start with "Sol" (case-insensitive)
	for _, sys := range systems {
		if len(sys.Name) < 3 {
			t.Errorf("system name '%s' is too short", sys.Name)
			continue
		}
		prefix := sys.Name[:3]
		if prefix != "Sol" && prefix != "sol" && prefix != "SOL" {
			t.Errorf("system '%s' does not start with 'Sol'", sys.Name)
		}
	}

	t.Logf("Found %d systems starting with 'Sol'", len(systems))
}

// BenchmarkGetSystemsInBounds measures spatial query performance.
func BenchmarkGetSystemsInBounds(b *testing.B) {
	host := os.Getenv("MEMGRAPH_TEST_HOST")
	if host == "" {
		b.Skip("MEMGRAPH_TEST_HOST not set")
	}

	cfg := Config{Host: host, Port: 7687}
	client, err := NewClient(cfg)
	if err != nil {
		b.Fatalf("failed to create client: %v", err)
	}
	defer client.Close(context.Background())

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		b.Skipf("could not connect: %v", err)
	}

	req := GalaxyViewRequest{
		MinX:  -500,
		MaxX:  500,
		MinY:  -500,
		MaxY:  500,
		MinZ:  -500,
		MaxZ:  500,
		Limit: 10000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := client.GetSystemsInBounds(ctx, req)
		if err != nil {
			b.Fatalf("query failed: %v", err)
		}
	}
}

// BenchmarkGetSystemDetail measures system detail query performance.
func BenchmarkGetSystemDetail(b *testing.B) {
	host := os.Getenv("MEMGRAPH_TEST_HOST")
	if host == "" {
		b.Skip("MEMGRAPH_TEST_HOST not set")
	}

	cfg := Config{Host: host, Port: 7687}
	client, err := NewClient(cfg)
	if err != nil {
		b.Fatalf("failed to create client: %v", err)
	}
	defer client.Close(context.Background())

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		b.Skipf("could not connect: %v", err)
	}

	// Get Sol's ID for benchmarking
	solID, err := client.GetSystemIDByName(ctx, "Sol")
	if err != nil {
		b.Skipf("Sol not found: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.GetSystemDetail(ctx, solID)
		if err != nil {
			b.Fatalf("query failed: %v", err)
		}
	}
}
