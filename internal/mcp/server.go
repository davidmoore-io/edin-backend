package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/ops"
	"github.com/edin-space/edin-backend/internal/tools"
)

// Run starts the MCP server that exposes control tools.
func Run(ctx context.Context, cfg *config.Config, opsManager *ops.Manager, store llm.SessionBackend, llmClient *anthropic.Client, toolExec *tools.Executor, runner *assistant.Runner) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if opsManager == nil {
		return fmt.Errorf("ops manager is nil")
	}

	server := newServer(cfg.HTTP.InternalKey, opsManager, store, llmClient, toolExec, runner, cfg.LLM.SystemPrompt, cfg.LLM.MaxIterations, cfg.LLM.Store)
	streamable := mcpserver.NewStreamableHTTPServer(
		server.mcp,
		mcpserver.WithHTTPContextFunc(server.injectAuthContext),
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- streamable.Start(cfg.HTTP.MCPAddress)
	}()

	if cfg.EnableMCPStdIO {
		go func() {
			if err := mcpserver.ServeStdio(server.mcp); err != nil && !errors.Is(err, context.Canceled) {
				server.logger.Warn(fmt.Sprintf("MCP stdio server exited: %v", err))
			}
		}()
	}

	server.logger.Info(fmt.Sprintf("MCP server listening on %s", cfg.HTTP.MCPAddress))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = streamable.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type server struct {
	apiKey    string
	mcp       *mcpserver.MCPServer
	ops       *ops.Manager
	llmStore  llm.SessionBackend
	llmClient *anthropic.Client
	toolExec  *tools.Executor
	llmRunner *assistant.Runner
	logger    *observability.Logger
	storeCfg  config.ConversationStoreConfig
}

func newServer(apiKey string, opsManager *ops.Manager, store llm.SessionBackend, llmClient *anthropic.Client, toolExec *tools.Executor, runner *assistant.Runner, systemPrompt string, maxIterations int, storeCfg config.ConversationStoreConfig) *server {
	s := &server{
		apiKey:    apiKey,
		ops:       opsManager,
		llmStore:  store,
		llmClient: llmClient,
		toolExec:  toolExec,
		llmRunner: runner,
		logger:    observability.NewLogger("mcp"),
		storeCfg:  storeCfg,
	}

	if s.toolExec == nil {
		s.toolExec = tools.NewExecutor(opsManager, nil, nil, nil)
	}
	if s.llmRunner == nil && llmClient != nil {
		s.llmRunner = assistant.NewRunner(llmClient, s.toolExec, systemPrompt, maxIterations)
	}

	mcpSrv := mcpserver.NewMCPServer(
		"ssg-control-mcp",
		"1.0.0",
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithPromptCapabilities(true),
	)

	for _, tool := range tools.MCPToolDefinitions() {
		tool := tool
		mcpSrv.AddTool(tool, s.wrapTool(tool.Name))
	}
	if llmClient != nil && store != nil && s.llmRunner != nil {
		mcpSrv.AddTool(newLLMTool(), s.handleLLM)
	}

	mcpSrv.AddResource(
		mcp.NewResource(
			"control://services",
			"Managed Services",
			mcp.WithResourceDescription("Inventory of known Docker services and curated Ansible playbooks."),
			mcp.WithMIMEType("application/json"),
		),
		s.readServicesResource,
	)

	mcpSrv.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"control://logs/{service}",
			"Service Logs",
			mcp.WithTemplateDescription("Recent log excerpts for a managed service."),
			mcp.WithTemplateMIMEType("application/json"),
		),
		s.readLogsResource,
	)

	mcpSrv.AddPrompt(mcp.NewPrompt("investigate_service",
		mcp.WithPromptDescription("Investigate the health of a managed service and surface any anomalies."),
		mcp.WithArgument("service",
			mcp.ArgumentDescription("Service identifier to investigate"),
			mcp.RequiredArgument(),
		),
	), s.promptInvestigateService)

	mcpSrv.AddPrompt(mcp.NewPrompt("summarize_logs",
		mcp.WithPromptDescription("Summarize the latest log stream for a service."),
		mcp.WithArgument("service",
			mcp.ArgumentDescription("Service identifier whose logs should be summarized"),
			mcp.RequiredArgument(),
		),
	), s.promptSummarizeLogs)

	s.mcp = mcpSrv
	return s
}

func (s *server) wrapTool(name string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !s.ensureAuthorized(ctx) {
			return mcp.NewToolResultError("unauthorized"), nil
		}
		if s.toolExec == nil {
			return mcp.NewToolResultError("tool executor unavailable"), nil
		}
		toolName := request.Params.Name
		if toolName == "" {
			toolName = name
		}
		args := request.GetArguments()
		if args == nil {
			args = map[string]any{}
		}
		result, err := s.toolExec.Invoke(ctx, toolName, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		payload, err := mcp.NewToolResultJSON(result)
		if err != nil {
			return nil, err
		}
		return payload, nil
	}
}

func newStatusTool() mcp.Tool {
	return mcp.NewTool("status_service",
		mcp.WithDescription("Fetch container status for a managed service"),
		mcp.WithString("service", mcp.Required(), mcp.Description("Service name, e.g. 'dayz'")),
	)
}

func newRestartTool() mcp.Tool {
	return mcp.NewTool("restart_service",
		mcp.WithDescription("Restart a managed container"),
		mcp.WithString("service", mcp.Required(), mcp.Description("Service name to restart")),
	)
}

func newLogsTool() mcp.Tool {
	return mcp.NewTool("tail_logs",
		mcp.WithDescription("Retrieve the most recent logs for a service"),
		mcp.WithString("service", mcp.Required(), mcp.Description("Service name")),
		mcp.WithNumber("limit", mcp.Description("Number of log lines to return (default 200)")),
	)
}

func newAnsibleTool() mcp.Tool {
	return mcp.NewTool("run_ansible",
		mcp.WithDescription("Execute an allow-listed Ansible playbook"),
		mcp.WithString("playbook", mcp.Required(), mcp.Description("Playbook identifier")),
		mcp.WithObject("extra_vars", mcp.Description("Additional variables for the playbook")),
	)
}

func newLLMTool() mcp.Tool {
	return mcp.NewTool("llm_message",
		mcp.WithDescription("Send a message via the Anthropic-backed LLM assistant"),
		mcp.WithString("session_id", mcp.Description("Existing session ID, if continuing a conversation")),
		mcp.WithString("user_id", mcp.Required(), mcp.Description("Unique user identifier")),
		mcp.WithString("message", mcp.Required(), mcp.Description("User message")),
	)
}

type authKeyType struct{}

var authKey authKeyType

func (s *server) injectAuthContext(ctx context.Context, r *http.Request) context.Context {
	authHeader := r.Header.Get("Authorization")
	authorized := strings.HasPrefix(authHeader, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer ")) == s.apiKey
	ctx = context.WithValue(ctx, authKey, authorized)
	if authorized {
		ctx = authz.ContextWithScopes(ctx, authz.ScopeAdmin, authz.ScopeLlmOperator)
	}
	return ctx
}

func (s *server) ensureAuthorized(ctx context.Context) bool {
	val, ok := ctx.Value(authKey).(bool)
	return ok && val
}

func (s *server) handleStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.ensureAuthorized(ctx) {
		return mcp.NewToolResultError("unauthorized"), nil
	}
	service, err := request.RequireString("service")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	status, err := s.ops.ServiceStatus(ctx, service)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	result, err := mcp.NewToolResultJSON(status)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *server) handleRestart(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.ensureAuthorized(ctx) {
		return mcp.NewToolResultError("unauthorized"), nil
	}
	service, err := request.RequireString("service")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	result, err := s.ops.RestartService(ctx, service)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	toolResult, err := mcp.NewToolResultJSON(result)
	if err != nil {
		return nil, err
	}
	return toolResult, nil
}

func (s *server) handleLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.ensureAuthorized(ctx) {
		return mcp.NewToolResultError("unauthorized"), nil
	}
	service, err := request.RequireString("service")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := request.GetInt("limit", s.ops.LogTailDefault())
	entries, err := s.ops.TailLogs(ctx, service, limit)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	payload := map[string]any{
		"service": service,
		"entries": entries,
	}
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *server) handleAnsible(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !s.ensureAuthorized(ctx) {
		return mcp.NewToolResultError("unauthorized"), nil
	}
	playbook, err := request.RequireString("playbook")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	extraVars := make(map[string]string)
	if args := request.GetArguments(); args != nil {
		if ev, ok := args["extra_vars"]; ok {
			switch typed := ev.(type) {
			case map[string]any:
				for k, v := range typed {
					if str, ok := v.(string); ok {
						extraVars[k] = str
					}
				}
			case map[string]string:
				extraVars = typed
			case json.RawMessage:
				_ = json.Unmarshal(typed, &extraVars)
			}
		}
	}

	job, err := s.ops.RunPlaybook(ctx, playbook, extraVars)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	result, err := mcp.NewToolResultJSON(job)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *server) handleLLM(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.llmClient == nil || s.llmStore == nil {
		return mcp.NewToolResultError("llm support unavailable"), nil
	}
	if !s.ensureAuthorized(ctx) {
		return mcp.NewToolResultError("unauthorized"), nil
	}

	userID, err := request.RequireString("user_id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	message, err := request.RequireString("message")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	sessionID := request.GetString("session_id", "")

	session, ok := s.llmStore.Get(sessionID)
	if !ok {
		session = s.llmStore.CreateSession(userID)
	}

	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return mcp.NewToolResultError("message cannot be empty"), nil
	}

	history := s.trimHistory(session.Messages)

	var reply string
	if s.llmRunner != nil {
		reply, err = s.llmRunner.Run(ctx, history, trimmed)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	} else {
		messages := llm.ToAnthropicMessages(history)
		messages = append(messages, anthropic.Message{
			Role:    "user",
			Content: trimmed,
		})
		resp, err := s.llmClient.Complete(ctx, anthropic.ChatRequest{
			SessionID: session.ID,
			Messages:  messages,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reply = resp.Content
	}

	userMessage := llm.Message{
		Role:      "user",
		Content:   trimmed,
		CreatedAt: time.Now().UTC(),
	}
	if updated, err := s.llmStore.AppendMessage(session.ID, userMessage); err == nil {
		session = updated
		session.Messages = s.trimHistory(session.Messages)
	} else {
		return mcp.NewToolResultError(err.Error()), nil
	}

	assistantMessage := llm.Message{
		Role:      "assistant",
		Content:   reply,
		CreatedAt: time.Now().UTC(),
	}
	if updated, err := s.llmStore.AppendMessage(session.ID, assistantMessage); err == nil {
		session = updated
		session.Messages = s.trimHistory(session.Messages)
	} else {
		return mcp.NewToolResultError(err.Error()), nil
	}

	payload := map[string]any{
		"session": session,
		"reply":   reply,
	}
	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *server) trimHistory(messages []llm.Message) []llm.Message {
	limit := s.storeCfg.MaxMessages
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	start := len(messages) - limit
	trimmed := make([]llm.Message, limit)
	copy(trimmed, messages[start:])
	return trimmed
}

func (s *server) readServicesResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	payload := map[string]any{
		"services":     s.ops.ServiceNames(),
		"playbooks":    s.ops.PlaybookNames(),
		"generated_at": time.Now().UTC(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

func (s *server) readLogsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	service := strings.TrimPrefix(request.Params.URI, "control://logs/")
	if err := s.ensureService(service); err != nil {
		return nil, err
	}

	limit := s.ops.LogTailDefault()
	if raw, ok := request.Params.Arguments["limit"]; ok {
		switch v := raw.(type) {
		case float64:
			if v > 0 {
				limit = int(v)
			}
		case string:
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
	}

	entries, err := s.ops.TailLogs(ctx, service, limit)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"service":      service,
		"limit":        limit,
		"generated_at": time.Now().UTC(),
		"entries":      entries,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

func (s *server) promptInvestigateService(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	service := strings.TrimSpace(request.Params.Arguments["service"])
	if err := s.ensureService(service); err != nil {
		return nil, err
	}

	description := fmt.Sprintf("Investigate the '%s' service", service)
	body := fmt.Sprintf(`The service "%s" may be experiencing issues.

Use the available control API tools to:
- Retrieve the current service status (tools/call status_service).
- Review recent logs (resources/read control://logs/%s).
- Recommend actionable remediation steps if problems are detected.

Respond with a concise summary and next steps.`, service, service)

	return mcp.NewGetPromptResult(description, []mcp.PromptMessage{
		mcp.NewPromptMessage(
			mcp.RoleUser,
			mcp.NewTextContent(body),
		),
	}), nil
}

func (s *server) promptSummarizeLogs(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	service := strings.TrimSpace(request.Params.Arguments["service"])
	if err := s.ensureService(service); err != nil {
		return nil, err
	}

	description := fmt.Sprintf("Summarize recent logs for '%s'", service)
	body := fmt.Sprintf(`Fetch control://logs/%s and provide:
- A high-level summary of the most recent activity
- Error or warning highlights
- Suggested follow-up actions (if any)

Keep the response focused and actionable.`, service)

	return mcp.NewGetPromptResult(description, []mcp.PromptMessage{
		mcp.NewPromptMessage(
			mcp.RoleUser,
			mcp.NewTextContent(body),
		),
	}), nil
}

func (s *server) ensureService(service string) error {
	service = strings.TrimSpace(service)
	if service == "" {
		return fmt.Errorf("service is required")
	}
	for _, candidate := range s.ops.ServiceNames() {
		if candidate == service {
			return nil
		}
	}
	return fmt.Errorf("unknown service %q", service)
}
