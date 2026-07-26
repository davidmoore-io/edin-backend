package httpapi

import (
	"fmt"

	"github.com/edin-space/edin-backend/internal/llm"
)

func loadProviderContext(store llm.SessionBackend, sessionID, provider string) (llm.ProviderContext, error) {
	contextStore, ok := store.(llm.ProviderContextBackend)
	if !ok {
		return llm.ProviderContext{}, fmt.Errorf("chat persistence does not support provider context")
	}
	context, found, err := contextStore.GetProviderContext(sessionID, provider)
	if err != nil {
		return llm.ProviderContext{}, err
	}
	if !found {
		return llm.ProviderContext{}, nil
	}
	return context, nil
}

func commitAssistantTurn(
	store llm.SessionBackend,
	sessionID string,
	message llm.Message,
	context llm.ProviderContext,
) (*llm.Session, error) {
	contextStore, ok := store.(llm.ProviderContextBackend)
	if !ok {
		return nil, fmt.Errorf("chat persistence does not support provider context")
	}
	return contextStore.CommitAssistantTurn(sessionID, message, context)
}
