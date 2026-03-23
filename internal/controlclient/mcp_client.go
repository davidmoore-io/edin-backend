package controlclient

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPClient provides access to MCP tools exposed by the control API.
type MCPClient struct {
	baseURL string
	apiKey  string

	mu          sync.Mutex
	client      *mcpclient.Client
	initialized bool
}

// NewMCPClient creates a new MCP client instance.
func NewMCPClient(baseURL, apiKey string) (*MCPClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("baseURL cannot be empty")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api key cannot be empty")
	}
	return &MCPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
	}, nil
}

// CallTool executes an MCP tool invocation.
// If the session is stale (server restarted), it will automatically reconnect.
func (c *MCPClient) CallTool(ctx context.Context, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	if err := c.ensureReady(ctx); err != nil {
		return nil, err
	}
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	}
	result, err := c.client.CallTool(ctx, request)
	if err != nil && isSessionError(err) {
		// Session is stale (server restarted), reconnect and retry
		c.reset()
		if err := c.ensureReady(ctx); err != nil {
			return nil, err
		}
		return c.client.CallTool(ctx, request)
	}
	return result, err
}

// isSessionError checks if the error indicates a stale/terminated session.
func isSessionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "session terminated") ||
		strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "re-initialize")
}

// reset clears the initialized state so the next call will reconnect.
func (c *MCPClient) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = nil
	c.initialized = false
}

func (c *MCPClient) ensureReady(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.initialized && c.client != nil {
		return nil
	}

	// Always create fresh transport and client
	headers := map[string]string{
		"Authorization": "Bearer " + c.apiKey,
	}

	httpTransport, err := transport.NewStreamableHTTP(
		c.baseURL,
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPTimeout(60*time.Second),
	)
	if err != nil {
		return fmt.Errorf("create MCP HTTP transport: %w", err)
	}

	client := mcpclient.NewClient(httpTransport)
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start MCP client: %w", err)
	}

	initRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "ssg-discord-bot",
				Version: "1.0.0",
			},
		},
	}

	if _, err := client.Initialize(ctx, initRequest); err != nil {
		// Close the failed client to clean up
		_ = client.Close()
		return fmt.Errorf("initialize MCP client: %w", err)
	}

	c.client = client
	c.initialized = true
	return nil
}
