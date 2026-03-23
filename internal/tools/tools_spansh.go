package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/spansh"
)

// spanshQuery executes a Spansh API query.
func (e *Executor) spanshQuery(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (carrier routes are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.spansh == nil {
		return nil, errors.New("spansh integration not available")
	}
	op := spansh.Operation(strings.TrimSpace(getString(args, "operation")))
	if op == "" {
		return nil, errors.New("operation is required")
	}

	// Whitelist allowed operations - deprecated ops should use galaxy_* or inara_* tools
	allowedOps := map[spansh.Operation]bool{
		spansh.OpFleetCarrierRoute: true, // Keep - no local equivalent for route planning
		spansh.OpHealth:            true, // Keep - useful diagnostic
	}
	if !allowedOps[op] {
		alternatives := map[spansh.Operation]string{
			spansh.OpSystemLookup:        "Use galaxy_system or system_profile instead",
			spansh.OpPowerplaySystems:    "Use galaxy_power with include_systems=true instead",
			spansh.OpStationsSelling:     "Use inara_commodity_buy for commodity prices instead",
			spansh.OpFactionStateSystems: "Use galaxy_faction with faction_state parameter instead",
			spansh.OpGenericSystems:      "Use galaxy_system for system queries instead",
		}
		alt := alternatives[op]
		if alt == "" {
			alt = "Use galaxy_* tools for real-time EDIN data"
		}
		return map[string]any{
			"error":       fmt.Sprintf("operation %q is deprecated", op),
			"alternative": alt,
			"hint":        "The galaxy_* tools query EDIN which has more current data",
		}, nil
	}

	parameters := map[string]any{}
	if raw, ok := args["parameters"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			parameters = typed
		case json.RawMessage:
			_ = json.Unmarshal(typed, &parameters)
		}
	}
	result, err := e.spansh.Execute(ctx, op, parameters)
	if err != nil {
		return nil, err
	}
	result["operation"] = op
	return result, nil
}

// retrieveCarrierRoute retrieves a fleet carrier route result from Spansh.
func (e *Executor) retrieveCarrierRoute(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (carrier routes are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.spansh == nil {
		return nil, errors.New("spansh integration not available")
	}
	jobID := strings.TrimSpace(getString(args, "job_id"))
	if jobID == "" {
		return nil, errors.New("job_id is required")
	}
	payload, err := e.spansh.FleetCarrierResult(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return normalizeCarrierRoute(jobID, payload)
}

// normalizeCarrierRoute processes a fleet carrier route response into a friendlier format.
func normalizeCarrierRoute(jobID string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errors.New("fleet carrier route response was empty")
	}

	routeInfo := map[string]any{}
	if route := asMap(payload["route"]); route != nil {
		routeInfo["source"] = getString(route, "source")
		routeInfo["destinations"] = route["destinations"]
		routeInfo["capacity"] = route["capacity"]
		routeInfo["capacity_used"] = route["capacity_used"]
		routeInfo["name"] = getString(route, "name")

		jumps := asSlice(route["jumps"])
		summary := make([]map[string]any, 0, len(jumps))
		bullets := make([]string, 0, len(jumps))

		for idx, raw := range jumps {
			jump := asMap(raw)
			if jump == nil {
				continue
			}
			system := getString(jump, "name")
			if system == "" {
				system = fmt.Sprintf("Jump %d", idx+1)
			}
			distance := asFloat64(jump["distance"])
			fuelUsed := asFloat64(jump["fuel_used"])
			remaining := asFloat64(jump["distance_to_destination"])
			fuelInTank := asFloat64(jump["fuel_in_tank"])
			restock := asFloat64(jump["restock_amount"])
			tritiumMarket := asFloat64(jump["tritium_in_market"])
			mustRestock := asBool(jump["must_restock"])

			entry := map[string]any{
				"system":                   system,
				"distance_ly":              distance,
				"fuel_used_tonnes":         fuelUsed,
				"distance_remaining_ly":    remaining,
				"fuel_in_tank_tonnes":      fuelInTank,
				"must_restock":             mustRestock,
				"restock_amount_tonnes":    restock,
				"tritium_in_market_tonnes": tritiumMarket,
			}
			summary = append(summary, entry)

			var bullet string
			if distance <= 0 {
				bullet = fmt.Sprintf("%s – departure point, %.0f ly remaining, tank %.0f t", system, remaining, fuelInTank)
			} else {
				bullet = fmt.Sprintf("%s – jump %.1f ly, burn %.0f t (%.0f ly remaining)", system, distance, fuelUsed, remaining)
			}
			if mustRestock && restock > 0 {
				bullet += fmt.Sprintf("; restock %.0f t (market %.0f t)", restock, tritiumMarket)
			}
			bullets = append(bullets, bullet)
		}

		if len(summary) > 0 {
			payload["jumps"] = summary
		}

		numbered := make([]string, 0, len(bullets))
		for idx, bullet := range bullets {
			numbered = append(numbered, fmt.Sprintf("%d. %s", idx+1, bullet))
		}

		chunks := chunkStrings(numbered, 1800)
		chunkedMarkdown := make([]string, 0, len(chunks))
		totalChunks := len(chunks)
		for idx, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			chunkedMarkdown = append(chunkedMarkdown, fmt.Sprintf("Route segment %d/%d:\n%s", idx+1, totalChunks, chunk))
		}

		payload["bullets"] = bullets
		payload["numbered_bullets"] = numbered
		payload["bullet_chunks"] = chunks
		payload["chunked_markdown"] = chunkedMarkdown
		payload["full_markdown"] = strings.Join(numbered, "\n")
	}

	response := map[string]any{
		"job_id": jobID,
	}
	for k, v := range payload {
		if _, exists := response[k]; !exists {
			response[k] = v
		}
	}
	if len(routeInfo) > 0 {
		response["route"] = routeInfo
	}
	return response, nil
}
