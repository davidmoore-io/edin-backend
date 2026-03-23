package tools

import (
	"context"
	"errors"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyExpansionTargets finds optimal expansion targets for Kaine powerplay.
func (e *Executor) galaxyExpansionTargets(ctx context.Context, args map[string]any) (any, error) {
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}

	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}
	if e.kaineStore == nil {
		return nil, errors.New("kaine store not available - expansion data required")
	}

	result, err := e.kaineStore.FindExpansionTargets(ctx, e.memgraph, nil)
	if err != nil {
		return nil, err
	}

	return result, nil
}
