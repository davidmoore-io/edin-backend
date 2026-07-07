package tools

import (
	"context"
	"errors"
)

// galaxyPlasmiumBuyers finds Boom stations that buy Platinum/Osmium near Kaine mining maps.
// This implements Orok's Daily Process 1 for finding optimal Plasmium sell locations.
func (e *Executor) galaxyPlasmiumBuyers(ctx context.Context, args map[string]any) (any, error) {
	if e.galaxyStore == nil {
		return nil, errors.New("galaxy store not available")
	}
	if e.kaineStore == nil {
		return nil, errors.New("kaine store not available - mining maps required")
	}

	result, err := e.kaineStore.FindPlasmiumBuyers(ctx, e.galaxyStore, nil)
	if err != nil {
		return nil, err
	}

	return result, nil
}
