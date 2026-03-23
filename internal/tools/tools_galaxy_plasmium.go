package tools

import (
	"context"
	"errors"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyPlasmiumBuyers finds Boom stations that buy Platinum/Osmium near Kaine mining maps.
// This implements Orok's Daily Process 1 for finding optimal Plasmium sell locations.
func (e *Executor) galaxyPlasmiumBuyers(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}

	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}
	if e.kaineStore == nil {
		return nil, errors.New("kaine store not available - mining maps required")
	}

	// Call the plasmium buyers query from kaine store
	result, err := e.kaineStore.FindPlasmiumBuyers(ctx, e.memgraph, nil)
	if err != nil {
		return nil, err
	}

	return result, nil
}
