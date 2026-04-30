package memgraph

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGetSchema(t *testing.T) {
	client := skipIfNoMemgraph(t)
	defer client.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema, err := client.GetSchema(ctx)
	if err != nil {
		t.Fatalf("GetSchema failed: %v", err)
	}

	if len(schema.NodeLabels) == 0 {
		t.Error("expected node labels")
	}
	if len(schema.EdgeTypes) == 0 {
		t.Error("expected edge types")
	}
	if len(schema.Indexes) == 0 {
		t.Error("expected indexes")
	}
	if len(schema.NodeProperties) == 0 {
		t.Error("expected node properties to be populated for at least one label")
	}
	// System nodes should always have name + location at minimum.
	if sysProps, ok := schema.NodeProperties["System"]; ok {
		needs := []string{"name", "x", "y", "z"}
		for _, want := range needs {
			found := false
			for _, got := range sysProps {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("System.NodeProperties missing %q (got %v)", want, sysProps)
			}
		}
	}

	b, _ := json.MarshalIndent(schema, "", "  ")
	t.Logf("Schema:\n%s", string(b))
}
