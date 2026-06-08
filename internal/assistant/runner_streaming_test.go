package assistant_test

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/assistant"
)

func TestStreamingRunnerCallbacks_CanConstruct(t *testing.T) {
	var cb assistant.StreamingRunnerCallbacks
	cb.OnTextDelta = func(string) {}
	cb.OnSpeakChunk = func(string) {}
	cb.OnDataChunk = func(string) {}
	cb.OnProgress = func(assistant.ProgressEvent) {}
	_ = cb
}

func TestRunWithStreaming_MethodExists(t *testing.T) {
	_ = (*assistant.Runner).RunWithStreaming
}
