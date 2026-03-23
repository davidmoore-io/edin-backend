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

	b, _ := json.MarshalIndent(schema, "", "  ")
	t.Logf("Schema:\n%s", string(b))
}
