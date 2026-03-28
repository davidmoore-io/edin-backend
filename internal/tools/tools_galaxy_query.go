package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyQuery executes an ad-hoc Cypher query against Memgraph.
// Only read operations are allowed (no CREATE, DELETE, SET, etc.)
func (e *Executor) galaxyQuery(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	query := strings.TrimSpace(getString(args, "query"))
	if query == "" {
		return nil, errors.New("query is required")
	}

	// Validate query - block write operations
	sanitized, err := validateCypherQuery(query)
	if err != nil {
		return map[string]any{
			"error": err.Error(),
			"hint":  "Only read operations (MATCH, RETURN, WITH, WHERE, ORDER BY, LIMIT) are allowed",
		}, nil
	}

	// Parse parameters if provided
	params := make(map[string]any)
	if p, ok := args["parameters"].(map[string]any); ok {
		params = p
	}

	// Execute with timeout — 120s for complex queries on 3M+ node graph
	queryCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	start := time.Now()
	results, err := e.memgraph.ExecuteQuery(queryCtx, sanitized, params)
	elapsed := time.Since(start)

	if err != nil {
		return map[string]any{
			"error":             err.Error(),
			"query":             sanitized,
			"execution_time_ms": elapsed.Milliseconds(),
		}, nil
	}

	// Extract column names from first result
	var columns []string
	if len(results) > 0 {
		for key := range results[0] {
			columns = append(columns, key)
		}
	}

	return map[string]any{
		"query":             sanitized,
		"columns":           columns,
		"rows":              results,
		"row_count":         len(results),
		"execution_time_ms": elapsed.Milliseconds(),
		"source":            "memgraph",
	}, nil
}

// validateCypherQuery checks for forbidden operations and ensures LIMIT is present.
func validateCypherQuery(query string) (string, error) {
	upper := strings.ToUpper(query)

	// Block write operations
	forbidden := []string{"CREATE", "DELETE", "SET ", "REMOVE", "MERGE", "DROP", "DETACH"}
	for _, word := range forbidden {
		if strings.Contains(upper, word) {
			return "", fmt.Errorf("write operation not allowed: %s", word)
		}
	}

	// Block dangerous operations
	if strings.Contains(upper, "CALL ") {
		return "", errors.New("CALL procedures not allowed")
	}

	// Ensure LIMIT is present (or add one)
	if !strings.Contains(upper, "LIMIT") {
		query = strings.TrimSuffix(query, ";")
		query = query + " LIMIT 100"
	}

	return query, nil
}
