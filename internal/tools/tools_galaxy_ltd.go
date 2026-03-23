package tools

import (
	"context"
	"errors"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyLTDBuyers finds stations that buy Low Temperature Diamonds near Kaine mining maps.
func (e *Executor) galaxyLTDBuyers(ctx context.Context, args map[string]any) (any, error) {
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

	result, err := e.kaineStore.FindLTDBuyers(ctx, e.memgraph, nil)
	if err != nil {
		return nil, err
	}

	return result, nil
}
