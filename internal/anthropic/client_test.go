package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestCreateBetaMessageDoesNotRetryInvalidRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"type":"error",
			"error":{"type":"invalid_request_error","message":"invalid edit tag"},
			"request_id":"req_test"
		}`))
	}))
	defer server.Close()

	sdkClient := sdk.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL),
	)
	client := &Client{
		client:    &sdkClient,
		model:     sdk.Model("claude-sonnet-5"),
		maxTokens: 16,
	}

	_, err := client.CreateBetaMessage(context.Background(), sdk.BetaMessageNewParams{
		Messages: []sdk.BetaMessageParam{
			sdk.NewBetaUserMessage(sdk.NewBetaTextBlock("hello")),
		},
	})
	if err == nil {
		t.Fatal("invalid request unexpectedly succeeded")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("invalid request attempts = %d, want exactly 1", got)
	}
}
