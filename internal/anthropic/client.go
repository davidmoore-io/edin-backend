package anthropic

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Client wraps outbound Anthropic API interactions.
type Client struct {
	client    *sdk.Client
	model     sdk.Model
	maxTokens int64
}

// New constructs a Client instance configured for the given model.
func New(apiKey, model string, maxTokens int) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("anthropic api key is empty")
	}
	if model == "" {
		model = "claude-opus-4-6"
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	sdkClient := sdk.NewClient(option.WithAPIKey(apiKey))
	return &Client{
		client:    &sdkClient,
		model:     sdk.Model(model),
		maxTokens: int64(maxTokens),
	}, nil
}

// Message represents a conversational turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents a conversation turn.
type ChatRequest struct {
	SessionID string    `json:"session_id"`
	Messages  []Message `json:"messages"`
}

// ChatResponse encapsulates the assistant reply.
type ChatResponse struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

// Complete executes a chat completion against Claude.
func (c *Client) Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("anthropic client not initialised")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("no messages provided")
	}

	params, err := buildMessageParams(req.Messages)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Messages.New(ctx, sdk.MessageNewParams{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		Messages:  params,
	})
	if err != nil {
		return nil, err
	}

	content := extractText(resp.Content)
	return &ChatResponse{
		SessionID: req.SessionID,
		Content:   content,
	}, nil
}

func buildMessageParams(messages []Message) ([]sdk.MessageParam, error) {
	params := make([]sdk.MessageParam, 0, len(messages))
	for _, msg := range messages {
		textBlock := sdk.NewTextBlock(msg.Content)
		role := strings.ToLower(strings.TrimSpace(msg.Role))

		switch role {
		case "assistant":
			params = append(params, sdk.NewAssistantMessage(textBlock))
		case "system":
			params = append(params, sdk.NewAssistantMessage(textBlock))
		case "user", "":
			params = append(params, sdk.NewUserMessage(textBlock))
		default:
			return nil, fmt.Errorf("unsupported role %q", msg.Role)
		}
	}
	return params, nil
}

func extractText(blocks []sdk.ContentBlockUnion) string {
	for _, block := range blocks {
		if strings.EqualFold(block.Type, "text") && strings.TrimSpace(block.Text) != "" {
			return block.Text
		}
	}
	return ""
}

// Model exposes the configured model identifier.
func (c *Client) Model() sdk.Model {
	return c.model
}

// MaxTokens exposes the configured token limit per response.
func (c *Client) MaxTokens() int64 {
	return c.maxTokens
}

// CreateMessage issues a Messages API request with arbitrary parameters.
func (c *Client) CreateMessage(ctx context.Context, params sdk.MessageNewParams) (*sdk.Message, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("anthropic client not initialised")
	}
	if params.Model == "" {
		params.Model = c.model
	}
	if params.MaxTokens == 0 {
		params.MaxTokens = c.maxTokens
	}
	return c.client.Messages.New(ctx, params)
}

// CreateBetaMessage issues a Beta Messages API request with context management support.
func (c *Client) CreateBetaMessage(ctx context.Context, params sdk.BetaMessageNewParams) (*sdk.BetaMessage, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("anthropic client not initialised")
	}
	if params.Model == "" {
		params.Model = c.model
	}
	if params.MaxTokens == 0 {
		params.MaxTokens = c.maxTokens
	}
	return c.client.Beta.Messages.New(ctx, params)
}
