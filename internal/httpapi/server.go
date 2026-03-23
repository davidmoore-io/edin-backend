package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/dayz"
	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/ops"
	"github.com/edin-space/edin-backend/internal/security"
	"github.com/edin-space/edin-backend/internal/spansh"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/tools"
	ws "github.com/edin-space/edin-backend/internal/websocket"
)

// Run launches the HTTP API server with the provided dependencies.
func Run(ctx context.Context, cfg *config.Config, opsManager *ops.Manager, llmStore llm.SessionBackend, llmClient *anthropic.Client, toolExec *tools.Executor, runner *assistant.Runner, spanshClient *spansh.Client, cacheStore *store.CacheStore, wsHub *ws.Hub, memgraphClient *memgraph.Client, dayzService *dayz.Service, kaineStore *kaine.Store, eddnIntelStore *store.SystemIntelStore) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if opsManager == nil {
		return fmt.Errorf("ops manager is nil")
	}
	if llmStore == nil {
		return fmt.Errorf("llm store is nil")
	}

	metrics := observability.InitMetrics("ssg_control_api")

	// Use provided hub or create a new one
	if wsHub == nil {
		wsHub = ws.NewHub()
		go wsHub.Run()
	}

	server := &Server{
		cfg:            cfg,
		ops:            opsManager,
		llmStore:       llmStore,
		llmClient:      llmClient,
		logger:         observability.NewLogger("httpapi"),
		apiKey:         cfg.HTTP.InternalKey,
		metrics:        metrics,
		rateLimiter:    newRateLimiter(cfg.RateLimit.RequestsPerWindow, cfg.RateLimit.Window),
		toolExec:       toolExec,
		llmRunner:      runner,
		storeCfg:       cfg.LLM.Store,
		spansh:         spanshClient,
		cacheStore:     cacheStore,
		wsHub:          wsHub,
		memgraph:       memgraphClient,
		dayz:           dayzService,
		kaineStore:     kaineStore,
		eddnIntelStore: eddnIntelStore,
	}

	if server.toolExec == nil {
		server.toolExec = tools.NewExecutor(opsManager, nil, nil, nil)
	}
	// Wire up Memgraph client for galaxy database tools
	if server.memgraph != nil {
		server.toolExec = server.toolExec.WithMemgraph(server.memgraph)
	}
	// Wire up Kaine store for mining maps database access
	if server.kaineStore != nil {
		server.toolExec = server.toolExec.WithKaineStore(server.kaineStore)
	}
	if server.llmRunner == nil && llmClient != nil {
		server.llmRunner = assistant.NewRunner(llmClient, server.toolExec, cfg.LLM.SystemPrompt, cfg.LLM.MaxIterations)
	}
	// Create separate runner for Kaine chat with Elite Dangerous-only prompt (no ops tools)
	if server.kaineRunner == nil && llmClient != nil {
		server.kaineRunner = assistant.NewRunner(llmClient, server.toolExec, cfg.LLM.KaineSystemPrompt, cfg.LLM.MaxIterations)
	}

	// Initialize JWT validator for Kaine portal authentication
	if cfg.KaineAuth.Enabled {
		var err error
		server.jwtValidator, err = NewJWTValidator(cfg.KaineAuth, observability.NewLogger("jwt"))
		if err != nil {
			return fmt.Errorf("failed to initialize JWT validator: %w", err)
		}
		defer server.jwtValidator.Close()
	}

	// Initialize Authentik API client for user management
	if cfg.Authentik.Enabled && cfg.Authentik.Token != "" {
		server.authentikClient = authentik.NewClient(cfg.Authentik.URL, cfg.Authentik.Token)
		server.logger.Info("Authentik API client initialized")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", server.handleHealth)
	mux.HandleFunc("/status/", server.withAuth(server.handleStatus))
	mux.HandleFunc("/actions/", server.withAuth(server.handleAction))
	mux.HandleFunc("/logs/", server.withAuth(server.handleLogs))
	mux.HandleFunc("/ansible/run", server.withAuth(server.handleAnsible))
	mux.HandleFunc("/llm/session", server.withAuth(server.handleLLMSession))
	mux.HandleFunc("/spansh/powerplay-batch", server.withAuth(server.handlePowerplayBatch))
	mux.HandleFunc("/dayz/economy", server.withAuth(server.handleDayZEconomy)) // Internal route for Discord bot
	mux.Handle("/metrics", metrics.Handler())

	// EDIN Public API - No auth required for read-only data
	mux.HandleFunc("/api/edin/hip-thunderdome", server.handleHIPThunderdome)
	mux.HandleFunc("/api/edin/powerplay", server.handlePowerplay)
	mux.HandleFunc("/api/edin/power-standings", server.handlePowerStandings)
	mux.HandleFunc("/api/edin/inara-links", server.handleEDINInaraLinks)         // Inara IDs for direct links
	mux.HandleFunc("/api/edin/inara/latest", server.handleEDINInaraLinks)        // Legacy route (same handler)
	mux.HandleFunc("/api/edin/systems/", server.handleEDINSystemHistory)         // /api/edin/systems/{name}/history
	mux.HandleFunc("/api/edin/status", server.handleEDINStatus)
	mux.HandleFunc("/api/edin/openapi.json", server.handleEDINOpenAPI)
	mux.HandleFunc("/api/edin/ws", server.handleEDINWebSocket)
	mux.HandleFunc("/api/internal/system-updated", server.handleInternalSystemUpdated) // EDDN listener callback

	// DayZ Public API - No auth required for read-only data
	mux.HandleFunc("/api/dayz/status", server.handleDayZStatus)
	mux.HandleFunc("/api/dayz/spawns", server.handleDayZSpawns)
	mux.HandleFunc("/api/dayz/map-config", server.handleDayZMapConfig)
	mux.HandleFunc("/api/dayz/categories", server.handleDayZCategories)
	mux.HandleFunc("/api/dayz/refresh", server.handleDayZRefresh)
	mux.HandleFunc("/api/dayz/full", server.handleDayZFull)
	mux.HandleFunc("/api/dayz/economy", server.handleDayZEconomy)
	mux.HandleFunc("/api/dayz/items", server.handleDayZItemSearch)
	mux.HandleFunc("/dayz/items", server.withAuth(server.handleDayZItemSearch)) // Internal route for Discord bot

	// Kaine Portal API - Requires Authentik JWT auth
	server.RegisterKaineRoutes(mux)

	// Galaxy Visualization API
	server.RegisterGalaxyRoutes(mux)

	httpServer := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           server.applyMiddlewares(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.HTTP.EnableTLS {
			errCh <- httpServer.ListenAndServeTLS(cfg.HTTP.TLSCertPath, cfg.HTTP.TLSKeyPath)
		} else {
			errCh <- httpServer.ListenAndServe()
		}
	}()

	server.logger.Info(fmt.Sprintf("HTTP API listening on %s (tls=%v)", cfg.HTTP.Address, cfg.HTTP.EnableTLS))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Server holds HTTP handlers and shared dependencies.
type Server struct {
	cfg          *config.Config
	ops          *ops.Manager
	llmStore     llm.SessionBackend
	llmClient    *anthropic.Client
	logger       *observability.Logger
	apiKey       string
	metrics      *observability.Metrics
	rateLimiter  *rateLimiter
	toolExec     *tools.Executor
	llmRunner    *assistant.Runner // Discord ops runner (has access to system management tools)
	kaineRunner  *assistant.Runner // Kaine chat runner (Elite Dangerous tools only, no ops)
	storeCfg     config.ConversationStoreConfig
	spansh       *spansh.Client
	cacheStore   *store.CacheStore
	wsHub        *ws.Hub
	memgraph     *memgraph.Client
	dayz         *dayz.Service
	kaineStore   *kaine.Store
	jwtValidator TokenValidator
	eddnIntelStore *store.SystemIntelStore // EDDN raw feed queries for system intel
	authentikClient *authentik.Client       // Authentik API client for user management

	// Power standings cache (lazy-loaded, 15-minute TTL)
	standingsCacheMu    sync.RWMutex
	standingsCacheData  map[string]*store.PowerStandingResult // keyed by "granularity:hours"
	standingsCacheTimes map[string]time.Time                  // cache timestamps

	// All-powerplay cache (lazy-loaded, 60-second TTL)
	powerplayCacheMu   sync.RWMutex
	powerplayCacheData []map[string]any
	powerplayCacheTime time.Time
}

// WebSocket upgrader configuration
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow connections from our domain
		origin := r.Header.Get("Origin")
		return origin == "" ||
			strings.HasSuffix(origin, "ssg.sh") ||
			strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1")
	},
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			s.logger.Warn(fmt.Sprintf("unauthorized request: %s %s client_ip=%s", r.Method, r.URL.Path, clientIP(r)))
			s.writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if header == "" {
		return false
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return token == s.apiKey
}

func (s *Server) applyCORSHeaders(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.HTTP.AllowOrigins) == 0 {
		return
	}
	// CORS requires a single origin, not a comma-separated list.
	// Check if the request's Origin is in our allowed list.
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	for _, allowed := range s.cfg.HTTP.AllowOrigins {
		if origin == allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			break
		}
	}
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func (s *Server) rateLimitKey(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		return header
	}
	return clientIP(r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	s.writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "ssg-control-api",
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	service := strings.TrimPrefix(r.URL.Path, "/status/")
	if service == "" {
		s.writeError(w, http.StatusBadRequest, "service is required")
		return
	}
	status, err := s.ops.ServiceStatus(r.Context(), service)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/actions/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 || parts[1] != "restart" {
		s.writeError(w, http.StatusNotFound, "unknown action")
		return
	}
	service := parts[0]
	result, err := s.ops.RestartService(r.Context(), service)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	service := strings.TrimPrefix(r.URL.Path, "/logs/")
	service = strings.TrimSpace(service)
	if idx := strings.Index(service, "?"); idx >= 0 {
		service = service[:idx]
	}
	service = strings.TrimSpace(service)
	if service == "" {
		s.writeError(w, http.StatusBadRequest, "service is required")
		return
	}
	tail := s.cfg.Operations.LogTailDefault
	if v := r.URL.Query().Get("tail"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			tail = parsed
		}
	}
	entries, err := s.ops.TailLogs(r.Context(), service, tail)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"service": service,
		"entries": entries,
	})
}

func (s *Server) handleAnsible(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	var req struct {
		Playbook  string            `json:"playbook"`
		ExtraVars map[string]string `json:"extra_vars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	job, err := s.ops.RunPlaybook(r.Context(), req.Playbook, req.ExtraVars)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleLLMSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.logger.Warn("llm_session rejected non-POST request")
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}
	if s.llmClient == nil {
		s.logger.Warn("llm_session request rejected because llm integration is disabled")
		s.writeError(w, http.StatusServiceUnavailable, "llm integration disabled")
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		UserID    string `json:"user_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.logger.Warn(fmt.Sprintf("llm_session invalid payload: %v", err))
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		s.logger.Warn("llm_session missing user_id")
		s.writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	trimmedMessage := strings.TrimSpace(req.Message)
	if trimmedMessage == "" {
		s.logger.Warn("llm_session missing message content")
		s.writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	session, ok := s.llmStore.Get(req.SessionID)
	if !ok {
		session = s.llmStore.CreateSession(req.UserID)
		s.logger.Info(fmt.Sprintf("llm_session created new session=%s user=%s requested_session=%s", session.ID, req.UserID, req.SessionID))
	} else {
		s.logger.Info(fmt.Sprintf("llm_session resume session=%s user=%s history=%d", session.ID, req.UserID, len(session.Messages)))
	}

	history := s.trimHistory(session.Messages)
	ctx := assistant.WithContext(r.Context(), session.ID, req.UserID)
	ctx = authz.ContextWithScopes(ctx, authz.ScopeLlmOperator)
	start := time.Now()
	messagePreview := observability.Sanitize(trimmedMessage, 160)
	s.logger.Info(fmt.Sprintf("llm_session message session=%s user=%s history=%d message=\"%s\"", session.ID, req.UserID, len(history), messagePreview))

	var reply string
	if s.llmRunner != nil {
		var err error
		reply, err = s.llmRunner.Run(ctx, history, trimmedMessage)
		if err != nil {
			s.logger.Error(fmt.Sprintf("llm_session runner_error session=%s user=%s", session.ID, req.UserID), err)
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	} else {
		messages := llm.ToAnthropicMessages(history)
		messages = append(messages, anthropic.Message{
			Role:    "user",
			Content: trimmedMessage,
		})
		resp, err := s.llmClient.Complete(ctx, anthropic.ChatRequest{
			SessionID: session.ID,
			Messages:  messages,
		})
		if err != nil {
			s.logger.Error(fmt.Sprintf("llm_session complete_error session=%s user=%s", session.ID, req.UserID), err)
			s.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		reply = resp.Content
	}

	userMsg := llm.Message{
		Role:      "user",
		Content:   trimmedMessage,
		CreatedAt: time.Now().UTC(),
	}
	if updated, err := s.llmStore.AppendMessage(session.ID, userMsg); err == nil {
		session = updated
		session.Messages = s.trimHistory(session.Messages)
	} else {
		s.logger.Error(fmt.Sprintf("llm_session store_user_error session=%s user=%s", session.ID, req.UserID), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   reply,
		CreatedAt: time.Now().UTC(),
	}
	if updated, err := s.llmStore.AppendMessage(session.ID, assistantMsg); err == nil {
		session = updated
		session.Messages = s.trimHistory(session.Messages)
	} else {
		s.logger.Error(fmt.Sprintf("llm_session store_assistant_error session=%s user=%s", session.ID, req.UserID), err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	replyPreview := observability.Sanitize(reply, 200)
	s.logger.Info(fmt.Sprintf("llm_session success session=%s user=%s history=%d reply=\"%s\" duration=%s", session.ID, req.UserID, len(session.Messages), replyPreview, time.Since(start)))

	s.writeJSON(w, http.StatusOK, map[string]any{
		"session": session,
		"reply":   reply,
	})
}

func (s *Server) trimHistory(messages []llm.Message) []llm.Message {
	limit := s.storeCfg.MaxMessages
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	start := len(messages) - limit
	trimmed := make([]llm.Message, limit)
	copy(trimmed, messages[start:])
	return trimmed
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	// Note: CORS headers are already applied by middleware.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}

// sseProgressFunc returns a ProgressFunc that streams SSE events if the client
// requested text/event-stream via the Accept header. Returns nil if the client
// wants regular JSON — callers should pass nil to store methods in that case.
func (s *Server) sseProgressFunc(w http.ResponseWriter, r *http.Request) func(int, int, string) {
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return nil
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	return func(step int, total int, message string) {
		data, _ := json.Marshal(map[string]any{
			"step":    step,
			"total":   total,
			"message": message,
		})
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		flusher.Flush()
	}
}

// sseResult writes the final data event for an SSE stream.
func (s *Server) sseResult(w http.ResponseWriter, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		s.sseError(w, "failed to marshal result")
		return
	}
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// sseError writes an error event for an SSE stream.
func (s *Server) sseError(w http.ResponseWriter, message string) {
	data, _ := json.Marshal(map[string]string{"error": message})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// PowerplaySystemResult holds powerplay data for a single system.
type PowerplaySystemResult struct {
	Name                         string   `json:"name"`
	ControllingPower             string   `json:"controlling_power,omitempty"`
	Powers                       []string `json:"powers,omitempty"`
	PowerState                   string   `json:"power_state,omitempty"`
	PowerStateControlProgress    float64  `json:"power_state_control_progress,omitempty"`
	PowerStateReinforcement      int      `json:"power_state_reinforcement,omitempty"`
	PowerStateUndermining        int      `json:"power_state_undermining,omitempty"`
	ControllingMinorFaction      string   `json:"controlling_minor_faction,omitempty"`
	ControllingMinorFactionState string   `json:"controlling_minor_faction_state,omitempty"`
	Allegiance                   string   `json:"allegiance,omitempty"`
	Error                        string   `json:"error,omitempty"`
}

// handlePowerplayBatch queries Spansh for powerplay data for multiple systems in parallel.
func (s *Server) handlePowerplayBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}
	if s.spansh == nil {
		s.writeError(w, http.StatusServiceUnavailable, "spansh integration not available")
		return
	}

	var req struct {
		Systems   []string `json:"systems"`
		BatchSize int      `json:"batch_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Systems) == 0 {
		s.writeError(w, http.StatusBadRequest, "systems list is required")
		return
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 10
	}

	s.logger.Info(fmt.Sprintf("powerplay_batch: querying %d systems in batches of %d", len(req.Systems), req.BatchSize))

	results := make([]PowerplaySystemResult, len(req.Systems))
	var wg sync.WaitGroup
	sem := make(chan struct{}, req.BatchSize) // Limit concurrency

	for i, systemName := range req.Systems {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire
			defer func() { <-sem }() // Release

			result := PowerplaySystemResult{Name: name}

			params := map[string]any{"system_name": name}
			data, err := s.spansh.Execute(r.Context(), spansh.OpSystemLookup, params)
			if err != nil {
				result.Error = err.Error()
				results[idx] = result
				return
			}

			// Extract result from response
			resultMap := extractSpanshResult(data)
			if resultMap == nil {
				result.Error = "no matching system found"
				results[idx] = result
				return
			}

			// Extract powerplay fields
			result.ControllingPower = getStringFromMap(resultMap, "controlling_power")
			result.Powers = getStringSliceFromMap(resultMap, "power")
			result.PowerState = getStringFromMap(resultMap, "power_state")
			result.PowerStateControlProgress = getFloatFromMap(resultMap, "power_state_control_progress")
			result.PowerStateReinforcement = getIntFromMap(resultMap, "power_state_reinforcement")
			result.PowerStateUndermining = getIntFromMap(resultMap, "power_state_undermining")
			result.ControllingMinorFaction = getStringFromMap(resultMap, "controlling_minor_faction")
			result.ControllingMinorFactionState = getStringFromMap(resultMap, "controlling_minor_faction_state")
			result.Allegiance = getStringFromMap(resultMap, "allegiance")

			results[idx] = result
		}(i, systemName)
	}

	wg.Wait()

	s.writeJSON(w, http.StatusOK, map[string]any{
		"systems": results,
		"count":   len(results),
	})
}

// extractSpanshResult extracts the result object from Spansh response.
func extractSpanshResult(data map[string]any) map[string]any {
	if result, ok := data["result"].(map[string]any); ok {
		return result
	}
	if results, ok := data["results"].([]any); ok && len(results) > 0 {
		if first, ok := results[0].(map[string]any); ok {
			return first
		}
	}
	return nil
}

func getStringFromMap(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringSliceFromMap(m map[string]any, key string) []string {
	if v, ok := m[key].([]any); ok {
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func getFloatFromMap(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func getIntFromMap(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}

type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*security.TokenBucket
	capacity int
	window   time.Duration
}

func newRateLimiter(capacity int, window time.Duration) *rateLimiter {
	if capacity <= 0 {
		capacity = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	return &rateLimiter{
		buckets:  make(map[string]*security.TokenBucket),
		capacity: capacity,
		window:   window,
	}
}

func (r *rateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket, ok := r.buckets[key]
	if !ok {
		bucket = security.NewTokenBucket(r.capacity, r.window)
		r.buckets[key] = bucket
	}
	return bucket.Allow()
}

// =============================================================================
// EDIN Public API Handlers - Elite Dangerous Intel Network
// =============================================================================

// cgSystems is the exact list of 49 systems in the Community Goal.
// Only these systems are returned by the EDIN API, even though we capture all Col 359 data.
// Note: Col 359 Sector IB-W d2-19 was removed - not part of the CG target area.
var cgSystems = map[string]bool{
	"Col 359 Sector RX-R c5-16": true,
	"Col 359 Sector RX-R c5-3":  true,
	"Col 359 Sector RX-R c5-17": true,
	"Col 359 Sector RX-R c5-15": true,
	"Col 359 Sector KW-V d2-20": true,
	"Col 359 Sector CE-N b9-2":  true,
	"Col 359 Sector CE-N b9-1":  true,
	"Col 359 Sector YI-N b9-4":  true,
	"Col 359 Sector BE-N b9-4":  true,
	"Col 359 Sector BE-N b9-2":  true,
	"Col 359 Sector KW-V d2-16": true,
	"Col 359 Sector AE-N b9-4":  true,
	"Col 359 Sector NR-T c4-17": true,
	"Col 359 Sector NR-T c4-15": true,
	"Col 359 Sector AJ-N b9-3":  true,
	"Col 359 Sector QX-R c5-11": true,
	"Col 359 Sector RX-R c5-2":  true,
	"Col 359 Sector FK-L b10-4": true,
	"Col 359 Sector RX-R c5-1":  true,
	"Col 359 Sector MR-T c4-8":  true,
	"Col 359 Sector FK-L b10-1": true,
	"Col 359 Sector QX-R c5-10": true,
	"Col 359 Sector FK-L b10-0": true,
	"Col 359 Sector RX-R c5-0":  true,
	"Col 359 Sector FK-L b10-3": true,
	"Col 359 Sector ZI-N b9-2":  true,
	"Col 359 Sector AJ-N b9-4":  true,
	"Col 359 Sector ZI-N b9-1":  true,
	"Col 359 Sector QX-R c5-12": true,
	"Col 359 Sector QX-R c5-13": true,
	"Col 359 Sector DP-L b10-3": true,
	"Col 359 Sector KW-V d2-17": true,
	"Col 359 Sector BE-N b9-1":  true,
	"Col 359 Sector BE-N b9-0":  true,
	"Col 359 Sector BE-N b9-3":  true,
	"Col 359 Sector FK-L b10-2": true,
	"Col 359 Sector RX-R c5-4":  true,
	"Col 359 Sector FK-L b10-5": true,
	"Col 359 Sector QX-R c5-8":  true,
	"Col 359 Sector EK-L b10-4": true,
	"Col 359 Sector QX-R c5-7":  true,
	"Col 359 Sector NR-T c4-14": true,
	"Col 359 Sector NR-T c4-13": true,
	"Col 359 Sector YI-N b9-5":  true,
	"Col 359 Sector ZI-N b9-3":  true,
	"Col 359 Sector AE-N b9-1":  true,
	"Col 359 Sector AE-N b9-3":  true,
	"Col 359 Sector AE-N b9-5":  true,
	"Col 359 Sector AE-N b9-0":  true,
}

// cgSystemNames returns a slice of CG system names.
func cgSystemNames() []string {
	names := make([]string, 0, len(cgSystems))
	for name := range cgSystems {
		names = append(names, name)
	}
	return names
}

// TickInfo contains information about the current Elite Dangerous powerplay tick.
type TickInfo struct {
	Number        int       // Tick number since epoch
	Start         time.Time // Start of current tick (Thursday 07:00 UTC)
	End           time.Time // End of current tick (next Thursday 07:00 UTC)
	HoursIntoTick float64   // Hours elapsed since tick start
}

// calculateCurrentTick calculates the current tick info.
// The Elite Dangerous powerplay tick occurs every Thursday at 07:00 UTC.
func calculateCurrentTick(now time.Time) TickInfo {
	// Epoch: First tick of Powerplay 2.0 (Thursday 2024-10-31 07:00 UTC)
	// You can adjust this to any known tick boundary
	epoch := time.Date(2024, 10, 31, 7, 0, 0, 0, time.UTC)

	// Calculate days since epoch
	daysSinceEpoch := now.Sub(epoch).Hours() / 24

	// Calculate tick number (each tick is 7 days)
	tickNumber := int(daysSinceEpoch / 7)

	// Calculate tick start and end
	tickStart := epoch.Add(time.Duration(tickNumber*7*24) * time.Hour)
	tickEnd := tickStart.Add(7 * 24 * time.Hour)

	// Calculate hours into tick
	hoursIntoTick := now.Sub(tickStart).Hours()

	return TickInfo{
		Number:        tickNumber,
		Start:         tickStart,
		End:           tickEnd,
		HoursIntoTick: hoursIntoTick,
	}
}

// handleHIPThunderdome returns the latest powerplay data for the HIP Thunderdome systems.
// This endpoint uses Memgraph for real-time EDDN data when available,
// falling back to the current_state table in TimescaleDB.
func (s *Server) handleHIPThunderdome(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	// Try Memgraph first (real-time graph database)
	if s.memgraph != nil {
		systems, err := s.memgraph.GetCGSystems(r.Context(), cgSystemNames())
		if err != nil {
			s.logger.Warn(fmt.Sprintf("Memgraph query failed, falling back to TimescaleDB: %v", err))
		} else if len(systems) > 0 {
			// Convert Memgraph data to frontend format
			data := make([]map[string]any, 0, len(systems))
			var latestUpdate time.Time

			for _, sys := range systems {
				system := map[string]any{
					"system_name":               sys.SystemName,
					"power_state":               sys.PowerplayState,
					"reinforcement":             sys.Reinforcement,
					"undermining":               sys.Undermining,
					"allegiance":                sys.Allegiance,
					"government":                sys.Government,
					"population":                sys.Population,
					"controlling_faction":       sys.ControllingFaction,
					"controlling_faction_state": sys.ControllingFactionState,
					"source":                    "ssg-eddn",
				}

				// Powerplay fields
				if sys.ControllingPower != "" {
					system["controlling_power"] = sys.ControllingPower
				}
				if len(sys.Powers) > 0 {
					system["powers"] = sys.Powers
				}
				if sys.ControlProgress != nil {
					system["control_progress"] = *sys.ControlProgress
				}

				// Expansion/Contested special handling
				state := strings.ToLower(sys.PowerplayState)
				system["is_expansion"] = state == "expansion"
				system["is_contested"] = state == "contested"

				// Conflict progress for expansion/contested systems
				if len(sys.PowerplayConflictProgress) > 0 {
					system["conflict_progress"] = sys.PowerplayConflictProgress
				}

				// Timestamps
				if !sys.LastEDDNUpdate.IsZero() {
					system["updated_at"] = sys.LastEDDNUpdate.Format(time.RFC3339)
					system["last_eddn_update"] = sys.LastEDDNUpdate.Format(time.RFC3339)
					if sys.LastEDDNUpdate.After(latestUpdate) {
						latestUpdate = sys.LastEDDNUpdate
					}
				}

				// Additional fields for CSV export
				system["x"] = sys.X
				system["y"] = sys.Y
				system["z"] = sys.Z
				system["security"] = sys.Security
				system["economy"] = sys.Economy
				system["has_large_pad"] = sys.HasLargePad
				system["nearest_station"] = sys.NearestStation
				system["nearest_station_ls"] = sys.NearestStationLs
				system["station_count"] = sys.StationCount

				// Calculated fields
				system["net_merits"] = sys.Reinforcement - sys.Undermining
				total := float64(sys.Reinforcement + sys.Undermining)
				if total > 0 {
					system["merit_ratio"] = float64(sys.Reinforcement) / total
				} else {
					system["merit_ratio"] = 0.5
				}
				// Distance from Sol (sqrt(x² + y² + z²))
				system["distance_from_sol"] = math.Sqrt(sys.X*sys.X + sys.Y*sys.Y + sys.Z*sys.Z)

				data = append(data, system)
			}

			// Calculate current tick info
			tickInfo := calculateCurrentTick(time.Now().UTC())

			s.writeJSON(w, http.StatusOK, map[string]any{
				"systems":         data,
				"count":           len(data),
				"last_refresh":    latestUpdate.Format(time.RFC3339),
				"source":          "ssg-eddn-memgraph",
				"tick_number":     tickInfo.Number,
				"tick_start":      tickInfo.Start.Format(time.RFC3339),
				"tick_end":        tickInfo.End.Format(time.RFC3339),
				"export_time":     time.Now().UTC().Format(time.RFC3339),
				"hours_into_tick": tickInfo.HoursIntoTick,
			})
			return
		}
	}

	// Fallback to TimescaleDB current_state table
	if s.cacheStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDIN cache not configured")
		return
	}

	states, lastRefresh, err := s.cacheStore.GetAllCurrentState(r.Context())
	if err != nil {
		s.logger.Error("edin_current_state error", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to frontend-compatible format, filtering to only CG systems
	data := make([]map[string]any, 0, len(cgSystems))
	for _, state := range states {
		if !cgSystems[state.SystemName] {
			continue
		}

		system := map[string]any{
			"system_name":   state.SystemName,
			"power_state":   state.PowerState,
			"is_expansion":  state.IsExpansion,
			"reinforcement": state.Reinforcement,
			"undermining":   state.Undermining,
			"updated_at":    state.LastUpdated.Format(time.RFC3339),
			"update_count":  state.UpdateCount,
			"source":        "ssg-eddn",
		}

		if state.ControllingPower != nil {
			system["controlling_power"] = *state.ControllingPower
		}
		if state.ControlProgress != nil {
			system["control_progress"] = *state.ControlProgress
		}
		if len(state.Powers) > 0 {
			system["powers"] = state.Powers
		}

		data = append(data, system)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"systems":      data,
		"count":        len(data),
		"last_refresh": lastRefresh.Format(time.RFC3339),
		"source":       "ssg-eddn",
	})
}

// powerplayCacheTTL is the cache duration for the all-powerplay endpoint.
const powerplayCacheTTL = 60 * time.Second

// handlePowerplay returns the latest powerplay data for ALL controlled systems.
// Memgraph-only (no TimescaleDB fallback) with a 60-second server-side cache.
func (s *Server) handlePowerplay(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Memgraph not configured")
		return
	}

	// Prevent browser caching — data changes frequently via EDDN
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	// Check cache
	s.powerplayCacheMu.RLock()
	if s.powerplayCacheData != nil && time.Since(s.powerplayCacheTime) < powerplayCacheTTL {
		cached := s.powerplayCacheData
		cacheAge := time.Since(s.powerplayCacheTime)
		s.powerplayCacheMu.RUnlock()
		s.logger.Info(fmt.Sprintf("powerplay cache hit age=%s systems=%d", cacheAge.Round(time.Second), len(cached)))

		tickInfo := calculateCurrentTick(time.Now().UTC())
		s.writeJSON(w, http.StatusOK, map[string]any{
			"systems":         cached,
			"count":           len(cached),
			"source":          "ssg-eddn-memgraph",
			"tick_number":     tickInfo.Number,
			"tick_start":      tickInfo.Start.Format(time.RFC3339),
			"tick_end":        tickInfo.End.Format(time.RFC3339),
			"export_time":     time.Now().UTC().Format(time.RFC3339),
			"hours_into_tick": tickInfo.HoursIntoTick,
		})
		return
	}
	s.powerplayCacheMu.RUnlock()

	// Cache miss — query Memgraph
	systems, err := s.memgraph.GetAllPowerplaySystems(r.Context())
	if err != nil {
		s.logger.Error("powerplay memgraph query failed", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to frontend format (same structure as handleHIPThunderdome)
	data := make([]map[string]any, 0, len(systems))
	var latestUpdate time.Time

	for _, sys := range systems {
		system := map[string]any{
			"system_name":               sys.SystemName,
			"power_state":               sys.PowerplayState,
			"reinforcement":             sys.Reinforcement,
			"undermining":               sys.Undermining,
			"allegiance":                sys.Allegiance,
			"government":                sys.Government,
			"population":                sys.Population,
			"controlling_faction":       sys.ControllingFaction,
			"controlling_faction_state": sys.ControllingFactionState,
			"source":                    "ssg-eddn",
		}

		if sys.ControllingPower != "" {
			system["controlling_power"] = sys.ControllingPower
		}
		if len(sys.Powers) > 0 {
			system["powers"] = sys.Powers
		}
		if sys.ControlProgress != nil {
			system["control_progress"] = *sys.ControlProgress
		}

		state := strings.ToLower(sys.PowerplayState)
		system["is_expansion"] = state == "expansion"
		system["is_contested"] = state == "contested"

		if len(sys.PowerplayConflictProgress) > 0 {
			system["conflict_progress"] = sys.PowerplayConflictProgress
		}

		if !sys.LastEDDNUpdate.IsZero() {
			system["updated_at"] = sys.LastEDDNUpdate.Format(time.RFC3339)
			system["last_eddn_update"] = sys.LastEDDNUpdate.Format(time.RFC3339)
			if sys.LastEDDNUpdate.After(latestUpdate) {
				latestUpdate = sys.LastEDDNUpdate
			}
		}

		system["x"] = sys.X
		system["y"] = sys.Y
		system["z"] = sys.Z
		system["security"] = sys.Security
		system["economy"] = sys.Economy
		// Station data not included in lean query (fetched per-system on demand)
		system["has_large_pad"] = sys.HasLargePad
		system["nearest_station"] = sys.NearestStation
		system["nearest_station_ls"] = sys.NearestStationLs
		system["station_count"] = sys.StationCount

		system["net_merits"] = sys.Reinforcement - sys.Undermining
		total := float64(sys.Reinforcement + sys.Undermining)
		if total > 0 {
			system["merit_ratio"] = float64(sys.Reinforcement) / total
		} else {
			system["merit_ratio"] = 0.5
		}
		system["distance_from_sol"] = math.Sqrt(sys.X*sys.X + sys.Y*sys.Y + sys.Z*sys.Z)

		data = append(data, system)
	}

	// Update cache
	s.powerplayCacheMu.Lock()
	s.powerplayCacheData = data
	s.powerplayCacheTime = time.Now()
	s.powerplayCacheMu.Unlock()

	tickInfo := calculateCurrentTick(time.Now().UTC())

	s.logger.Info(fmt.Sprintf("powerplay cache miss - queried %d systems from Memgraph", len(data)))
	s.writeJSON(w, http.StatusOK, map[string]any{
		"systems":         data,
		"count":           len(data),
		"last_refresh":    latestUpdate.Format(time.RFC3339),
		"source":          "ssg-eddn-memgraph",
		"tick_number":     tickInfo.Number,
		"tick_start":      tickInfo.Start.Format(time.RFC3339),
		"tick_end":        tickInfo.End.Format(time.RFC3339),
		"export_time":     time.Now().UTC().Format(time.RFC3339),
		"hours_into_tick": tickInfo.HoursIntoTick,
	})
}

// handlePowerStandings returns time-bucketed control point aggregations by controlling power.
// Query params: granularity (15m, 30m, 1h, 6h, 1d), hours (1-720)
// standingsCacheTTL is the cache duration for power standings data.
const standingsCacheTTL = 15 * time.Minute

func (s *Server) handlePowerStandings(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.cacheStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDIN cache not configured")
		return
	}

	// Parse query parameters
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "1h"
	}

	hours := 168 // Default 7 days
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed > 0 {
			hours = parsed
			if hours > 720 {
				hours = 720
			}
		}
	}

	// Build cache key
	cacheKey := fmt.Sprintf("%s:%d", granularity, hours)

	// Check cache under read lock
	s.standingsCacheMu.RLock()
	if s.standingsCacheData != nil {
		if cachedResult, ok := s.standingsCacheData[cacheKey]; ok {
			if cachedTime, timeOk := s.standingsCacheTimes[cacheKey]; timeOk {
				if time.Since(cachedTime) < standingsCacheTTL {
					s.standingsCacheMu.RUnlock()
					s.logger.Info(fmt.Sprintf("power_standings cache hit key=%s age=%s", cacheKey, time.Since(cachedTime).Round(time.Second)))
					s.writeJSON(w, http.StatusOK, cachedResult)
					return
				}
			}
		}
	}
	s.standingsCacheMu.RUnlock()

	// Cache miss or stale - query fresh data
	s.logger.Info(fmt.Sprintf("power_standings cache miss key=%s - querying database", cacheKey))
	cgSystems := cgSystemNames()
	result, err := s.cacheStore.GetPowerStandingData(r.Context(), cgSystems, granularity, hours)
	if err != nil {
		s.logger.Error("power_standings query failed", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Fetch state counts from Memgraph (live data for CG systems)
	if s.memgraph != nil {
		stateCounts, err := s.memgraph.GetPowerStateCountsForSystems(r.Context(), cgSystems)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("Memgraph state counts query failed: %v", err))
		} else if stateCounts != nil {
			// Apply state counts to each power's summary
			for powerName, stats := range result.Summary {
				if counts, ok := stateCounts[powerName]; ok {
					stats.StateCounts = store.PowerStateCounts{
						Expansion:  counts.States["Expansion"],
						Contested:  counts.States["Contested"],
						Exploited:  counts.States["Exploited"],
						Fortified:  counts.States["Fortified"],
						Stronghold: counts.States["Stronghold"],
						HomeSystem: counts.States["HomeSystem"],
						Controlled: counts.Total,
					}
					stats.SystemCount = counts.Total
					result.Summary[powerName] = stats
				}
			}
		}
	}

	// Update cache under write lock
	s.standingsCacheMu.Lock()
	if s.standingsCacheData == nil {
		s.standingsCacheData = make(map[string]*store.PowerStandingResult)
		s.standingsCacheTimes = make(map[string]time.Time)
	}
	s.standingsCacheData[cacheKey] = result
	s.standingsCacheTimes[cacheKey] = time.Now()
	s.standingsCacheMu.Unlock()

	s.writeJSON(w, http.StatusOK, result)
}

// handleEDINInaraLinks returns Inara IDs for all tracked systems (for direct links to Inara).
// All powerplay data comes from EDDN/Memgraph - this endpoint only provides linking metadata.
func (s *Server) handleEDINInaraLinks(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.cacheStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDIN cache not configured")
		return
	}

	allLinks, err := s.cacheStore.GetAllInaraLinks(r.Context())
	if err != nil {
		s.logger.Error("edin_inara_links error", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Filter to only CG systems (prevents stale data from removed systems)
	links := make([]*store.InaraLink, 0, len(cgSystems))
	for _, link := range allLinks {
		if cgSystems[link.SystemName] {
			links = append(links, link)
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"links": links,
		"count": len(links),
	})
}

// handleEDINSystemHistory returns historical data for a specific system.
// Routes:
//   - /api/edin/systems/{systemName}/history?hours=24 - merit history (reinforcement/undermining)
//   - /api/edin/systems/{systemName}/expansion-history?hours=168 - expansion conflict progress history
func (s *Server) handleEDINSystemHistory(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.cacheStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDIN cache not configured")
		return
	}

	// Parse system name and endpoint from path: /api/edin/systems/{name}/history or /expansion-history
	path := strings.TrimPrefix(r.URL.Path, "/api/edin/systems/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		s.writeError(w, http.StatusBadRequest, "invalid path, expected /api/edin/systems/{name}/history or /expansion-history")
		return
	}

	systemName := parts[0]
	endpoint := parts[1]
	if systemName == "" {
		s.writeError(w, http.StatusBadRequest, "system name is required")
		return
	}

	// Parse hours parameter
	hours := 24
	if endpoint == "expansion-history" {
		hours = 168 // Default to 7 days for expansion history
	}
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed > 0 {
			hours = parsed
			if hours > 720 { // Max 30 days (raw feed has ~60 day retention)
				hours = 720
			}
		}
	}

	switch endpoint {
	case "history":
		history, err := s.cacheStore.GetSystemHistory(r.Context(), systemName, hours)
		if err != nil {
			s.logger.Error(fmt.Sprintf("edin_system_history error for %s", systemName), err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Ensure we return [] not null for empty arrays
		if history == nil {
			history = []store.SystemHistoryEntry{}
		}

		s.writeJSON(w, http.StatusOK, map[string]any{
			"system_name": systemName,
			"hours":       hours,
			"data_points": len(history),
			"history":     history,
		})

	case "expansion-history":
		history, err := s.cacheStore.GetExpansionHistory(r.Context(), systemName, hours)
		if err != nil {
			s.logger.Error(fmt.Sprintf("edin_expansion_history error for %s", systemName), err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Ensure we return [] not null for empty arrays
		if history == nil {
			history = []store.ExpansionHistoryEntry{}
		}

		s.writeJSON(w, http.StatusOK, map[string]any{
			"system_name": systemName,
			"hours":       hours,
			"data_points": len(history),
			"history":     history,
		})

	default:
		s.writeError(w, http.StatusBadRequest, "invalid endpoint, expected 'history' or 'expansion-history'")
	}
}

// handleEDINStatus returns cache status information.
func (s *Server) handleEDINStatus(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.cacheStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "EDIN cache not configured")
		return
	}

	status, err := s.cacheStore.GetStatus(r.Context())
	if err != nil {
		s.logger.Error("edin_status error", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, status)
}

// handleEDINOpenAPI returns the OpenAPI specification for the EDIN API.
func (s *Server) handleEDINOpenAPI(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	openAPISpec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "EDIN - Elite Dangerous Intel Network API",
			"description": "Public API for Elite Dangerous powerplay intelligence data. Provides real-time and historical data from Spansh and Inara sources for Community Goal tracking.",
			"version":     "1.0.0",
			"contact": map[string]any{
				"name": "Sleeper Service Gaming",
				"url":  "https://ssg.sh",
			},
			"license": map[string]any{
				"name": "MIT",
			},
		},
		"servers": []map[string]any{
			{"url": "https://ssg.sh", "description": "Production"},
		},
		"paths": map[string]any{
			"/api/edin/hip-thunderdome": map[string]any{
				"get": map[string]any{
					"summary":     "Get HIP Thunderdome powerplay data",
					"description": "Returns real-time powerplay data from SSG's EDDN listener for all HIP Thunderdome systems. Updates within seconds of player activity.",
					"operationId": "getHIPThunderdome",
					"tags":        []string{"Powerplay Data"},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Successful response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/SpanshResponse",
									},
								},
							},
						},
						"503": map[string]any{
							"description": "Cache not configured",
						},
					},
				},
			},
			"/api/edin/power-standings": map[string]any{
				"get": map[string]any{
					"summary":     "Get power standings time series",
					"description": "Returns time-bucketed control point aggregations and summary statistics by power for the HIP Thunderdome systems.",
					"operationId": "getPowerStandings",
					"tags":        []string{"Powerplay Data"},
					"parameters": []map[string]any{
						{
							"name":        "granularity",
							"in":          "query",
							"required":    false,
							"description": "Time bucket granularity (15m, 30m, 1h, 6h, 1d)",
							"schema":      map[string]any{"type": "string", "default": "1h"},
						},
						{
							"name":        "hours",
							"in":          "query",
							"required":    false,
							"description": "Number of hours to return (default: 168, max: 720)",
							"schema":      map[string]any{"type": "integer", "default": 168, "maximum": 720},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Successful response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/PowerStandingsResponse",
									},
								},
							},
						},
						"503": map[string]any{
							"description": "Cache not configured",
						},
					},
				},
			},
			"/api/edin/inara-links": map[string]any{
				"get": map[string]any{
					"summary":     "Get Inara system links",
					"description": "Returns Inara IDs for all tracked systems, used to construct direct links to Inara. All powerplay data comes from EDDN/Memgraph.",
					"operationId": "getInaraLinks",
					"tags":        []string{"Metadata"},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Successful response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/InaraLinksResponse",
									},
								},
							},
						},
						"503": map[string]any{
							"description": "Cache not configured",
						},
					},
				},
			},
			"/api/edin/systems/{systemName}/history": map[string]any{
				"get": map[string]any{
					"summary":     "Get system history",
					"description": "Returns historical powerplay data for a specific system. Data is collected every 15 minutes and retained for 30 days.",
					"operationId": "getSystemHistory",
					"tags":        []string{"Historical Data"},
					"parameters": []map[string]any{
						{
							"name":        "systemName",
							"in":          "path",
							"required":    true,
							"description": "Name of the star system (URL encoded)",
							"schema":      map[string]any{"type": "string"},
							"example":     "Col 359 Sector FK-L b10-5",
						},
						{
							"name":        "hours",
							"in":          "query",
							"required":    false,
							"description": "Number of hours of history to return (default: 24, max: 168)",
							"schema":      map[string]any{"type": "integer", "default": 24, "maximum": 168},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Successful response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/HistoryResponse",
									},
								},
							},
						},
						"400": map[string]any{
							"description": "Invalid request",
						},
						"503": map[string]any{
							"description": "Cache not configured",
						},
					},
				},
			},
			"/api/edin/status": map[string]any{
				"get": map[string]any{
					"summary":     "Get cache status",
					"description": "Returns information about the cache including last refresh times and system counts.",
					"operationId": "getStatus",
					"tags":        []string{"Status"},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Successful response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/CacheStatus",
									},
								},
							},
						},
						"503": map[string]any{
							"description": "Cache not configured",
						},
					},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				"SpanshResponse": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"systems": map[string]any{
							"type":        "array",
							"description": "Array of system data from Spansh",
							"items": map[string]any{
								"$ref": "#/components/schemas/SpanshSystem",
							},
						},
						"count":        map[string]any{"type": "integer", "description": "Number of systems"},
						"last_refresh": map[string]any{"type": "string", "format": "date-time"},
						"source":       map[string]any{"type": "string", "enum": []string{"ssg-eddn"}},
					},
				},
				"SpanshSystem": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"system_name":       map[string]any{"type": "string"},
						"scraped_at":        map[string]any{"type": "string", "format": "date-time"},
						"controlling_power": map[string]any{"type": "string", "nullable": true},
						"power_state":       map[string]any{"type": "string"},
						"reinforcement":     map[string]any{"type": "integer", "description": "Total reinforcement merits"},
						"undermining":       map[string]any{"type": "integer", "description": "Total undermining merits"},
						"allegiance":        map[string]any{"type": "string"},
					},
				},
				"InaraLinksResponse": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"links": map[string]any{
							"type":        "array",
							"description": "Array of system name to Inara ID mappings",
							"items": map[string]any{
								"$ref": "#/components/schemas/InaraLink",
							},
						},
						"count": map[string]any{"type": "integer"},
					},
				},
				"InaraLink": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"system_name": map[string]any{"type": "string", "description": "Star system name"},
						"inara_id":    map[string]any{"type": "integer", "description": "Inara's internal ID for this system"},
					},
				},
				"HistoryResponse": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"system_name": map[string]any{"type": "string"},
						"hours":       map[string]any{"type": "integer"},
						"data_points": map[string]any{"type": "integer"},
						"history": map[string]any{
							"type": "array",
							"items": map[string]any{
								"$ref": "#/components/schemas/HistoryEntry",
							},
						},
					},
				},
				"HistoryEntry": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"timestamp":     map[string]any{"type": "string", "format": "date-time"},
						"reinforcement": map[string]any{"type": "integer"},
						"undermining":   map[string]any{"type": "integer"},
						"source":        map[string]any{"type": "string", "enum": []string{"ssg-eddn", "inara"}},
					},
				},
				"CacheStatus": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"eddn_last_refresh":  map[string]any{"type": "string", "format": "date-time"},
						"eddn_system_count":  map[string]any{"type": "integer"},
						"eddn_total_updates": map[string]any{"type": "integer"},
						"eddn_history_count": map[string]any{"type": "integer"},
						"inara_last_refresh": map[string]any{"type": "string", "format": "date-time"},
						"inara_system_count": map[string]any{"type": "integer"},
					},
				},
				"PowerStandingsResponse": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"time_series": map[string]any{
							"type": "array",
							"items": map[string]any{
								"$ref": "#/components/schemas/PowerStandingBucket",
							},
						},
						"summary": map[string]any{
							"type": "object",
							"additionalProperties": map[string]any{
								"$ref": "#/components/schemas/PowerStats",
							},
						},
						"gap_analysis": map[string]any{
							"$ref": "#/components/schemas/GapAnalysis",
						},
						"tick_info": map[string]any{
							"$ref": "#/components/schemas/TickInfo",
						},
						"query_params": map[string]any{
							"$ref": "#/components/schemas/QueryParams",
						},
						"data_through": map[string]any{"type": "string", "format": "date-time"},
						"generated_at": map[string]any{"type": "string", "format": "date-time"},
					},
				},
				"PowerStandingBucket": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"bucket": map[string]any{"type": "string", "format": "date-time"},
						"powers": map[string]any{
							"type": "object",
							"additionalProperties": map[string]any{
								"$ref": "#/components/schemas/ControlTotals",
							},
						},
					},
				},
				"ControlTotals": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"reinforcement": map[string]any{"type": "integer"},
						"undermining":   map[string]any{"type": "integer"},
					},
				},
				"PowerStats": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"current_total":       map[string]any{"type": "integer"},
						"first_total":         map[string]any{"type": "integer"},
						"delta":               map[string]any{"type": "integer"},
						"rate_per_hour":       map[string]any{"type": "number"},
						"first_observation":   map[string]any{"type": "string", "format": "date-time"},
						"last_observation":    map[string]any{"type": "string", "format": "date-time"},
						"system_count":        map[string]any{"type": "integer"},
						"reinforcement_total": map[string]any{"type": "integer"},
						"undermining_total":   map[string]any{"type": "integer"},
						"net_merits":          map[string]any{"type": "integer"},
						"state_counts": map[string]any{
							"$ref": "#/components/schemas/PowerStateCounts",
						},
					},
				},
				"PowerStateCounts": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expansion":   map[string]any{"type": "integer"},
						"contested":   map[string]any{"type": "integer"},
						"exploited":   map[string]any{"type": "integer"},
						"fortified":   map[string]any{"type": "integer"},
						"stronghold":  map[string]any{"type": "integer"},
						"home_system": map[string]any{"type": "integer"},
						"controlled":  map[string]any{"type": "integer"},
					},
				},
				"GapAnalysis": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"leader":                   map[string]any{"type": "string"},
						"leader_total":             map[string]any{"type": "integer"},
						"second":                   map[string]any{"type": "string"},
						"second_total":             map[string]any{"type": "integer"},
						"gap":                      map[string]any{"type": "integer"},
						"leader_rate":              map[string]any{"type": "number"},
						"second_rate":              map[string]any{"type": "number"},
						"projected_hours_to_close": map[string]any{"type": "number"},
					},
				},
				"TickInfo": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tick_number":     map[string]any{"type": "integer"},
						"tick_start":      map[string]any{"type": "string", "format": "date-time"},
						"tick_end":        map[string]any{"type": "string", "format": "date-time"},
						"hours_into_tick": map[string]any{"type": "number"},
					},
				},
				"QueryParams": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"granularity": map[string]any{"type": "string"},
						"hours":       map[string]any{"type": "integer"},
					},
				},
			},
		},
		"tags": []map[string]any{
			{"name": "Powerplay Data", "description": "Current powerplay intelligence from multiple sources"},
			{"name": "Historical Data", "description": "Time-series data for trend analysis"},
			{"name": "Status", "description": "API health and cache status"},
		},
	}

	s.writeJSON(w, http.StatusOK, openAPISpec)
}

// handleEDINWebSocket upgrades to WebSocket and registers the client for real-time updates.
func (s *Server) handleEDINWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("websocket upgrade failed", err)
		return
	}

	client := ws.NewClient(s.wsHub, conn)
	s.wsHub.Register(client)

	s.logger.Info(fmt.Sprintf("websocket client connected, total clients: %d", s.wsHub.ClientCount()))

	// Start client pumps in goroutines
	go client.WritePump()
	go client.ReadPump()
}

// handleInternalSystemUpdated receives notifications from the EDDN listener when a system
// is updated in Memgraph. It broadcasts via WebSocket so connected frontends can flash the row.
// Called via POST from the EDDN listener over WireGuard VPN.
func (s *Server) handleInternalSystemUpdated(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	var payload struct {
		SystemName string `json:"system_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.SystemName == "" {
		s.writeError(w, http.StatusBadRequest, "system_name required")
		return
	}

	if s.wsHub != nil && s.wsHub.ClientCount() > 0 {
		s.wsHub.BroadcastSystemUpdate(payload.SystemName, "ssg-eddn")
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetWebSocketHub returns the WebSocket hub for broadcasting updates.
func (s *Server) GetWebSocketHub() *ws.Hub {
	return s.wsHub
}
