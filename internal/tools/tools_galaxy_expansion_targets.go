package tools

import (
	"context"
	"errors"
)

// galaxyExpansionTargets finds optimal expansion targets for Kaine powerplay.
func (e *Executor) galaxyExpansionTargets(ctx context.Context, args map[string]any) (any, error) {
	if e.galaxyStore == nil {
		return nil, errors.New("galaxy store not available")
	}
	if e.kaineStore == nil {
		return nil, errors.New("kaine store not available - expansion data required")
	}

	result, err := e.kaineStore.FindExpansionTargets(ctx, e.galaxyStore, nil)
	if err != nil {
		return nil, err
	}

	return result, nil
}
