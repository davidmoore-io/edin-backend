package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/dayz"
	"github.com/edin-space/edin-backend/internal/edsm"
	"github.com/edin-space/edin-backend/internal/gameservers"
	"github.com/edin-space/edin-backend/internal/httpapi"
	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/mcp"
	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/ops"
	"github.com/edin-space/edin-backend/internal/spansh"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/tools"
	"github.com/edin-space/edin-backend/internal/websocket"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger := observability.NewLogger("control-api")

	opsManager, err := ops.NewManager(cfg.Operations, observability.NewLogger("ops"))
	if err != nil {
		log.Fatalf("initialise ops manager: %v", err)
	}

	llmStore, redisClient := buildConversationStore(cfg.LLM.Store)
	if redisClient != nil {
		defer redisClient.Close()
	}
	spanshClient := spansh.NewClient()
	edsmClient := edsm.NewClient()

	// NOTE: Inara scraping has been deprecated - see internal/deprecated/README.md
	// Data now comes from EDDN listener via Memgraph

	// Initialize EDIN database client (optional)
	var edinClient *store.Client
	var cacheStore *store.CacheStore
	var kaineStore *kaine.Store
	if cfg.EDIN.Enabled {
		storeLogger := observability.NewLogger("edin")
		client, err := store.New(ctx, store.Config{
			Enabled:  cfg.EDIN.Enabled,
			Host:     cfg.EDIN.Host,
			Port:     cfg.EDIN.Port,
			User:     cfg.EDIN.User,
			Password: cfg.EDIN.Password,
			Database: cfg.EDIN.Database,
			Schema:   cfg.EDIN.Schema,
			PoolSize: cfg.EDIN.PoolSize,
		}, store.WithLogger(func(msg string) {
			storeLogger.Info(msg)
		}))
		if err != nil {
			logger.Warn(fmt.Sprintf("EDIN database unavailable: %v (cache will use in-memory only)", err))
		} else {
			edinClient = client
			cacheStore = store.NewCacheStore(edinClient)
			// Initialize Kaine store using the same database pool (kaine schema is in EDIN DB)
			kaineStore = kaine.NewStore(edinClient.Pool())
			defer edinClient.Close()
			logger.Info("EDIN database connected")
			logger.Info("Kaine objectives store initialized")
		}
	}

	// Initialize EDDN raw feed database client (for historical powerplay data and system intel)
	var eddnIntelStore *store.SystemIntelStore
	if cfg.EDIN.EDDNRaw.Enabled {
		eddnLogger := observability.NewLogger("eddn-raw")
		eddnClient, err := store.New(ctx, store.Config{
			Enabled:  cfg.EDIN.EDDNRaw.Enabled,
			Host:     cfg.EDIN.EDDNRaw.Host,
			Port:     cfg.EDIN.EDDNRaw.Port,
			User:     cfg.EDIN.EDDNRaw.User,
			Password: cfg.EDIN.EDDNRaw.Password,
			Database: cfg.EDIN.EDDNRaw.Database,
			Schema:   cfg.EDIN.EDDNRaw.Schema,
			PoolSize: cfg.EDIN.EDDNRaw.PoolSize,
		}, store.WithLogger(func(msg string) {
			eddnLogger.Info(msg)
		}))
		if err != nil {
			logger.Warn(fmt.Sprintf("EDDN raw database unavailable: %v (historical queries disabled)", err))
		} else {
			if cacheStore != nil {
				cacheStore.SetEDDNClient(eddnClient)
			}
			eddnIntelStore = store.NewSystemIntelStore(eddnClient.Pool())
			defer eddnClient.Close()
			logger.Info("EDDN raw feed database connected")
			logger.Info("System intel store initialized")
		}
	}

	// Initialize Memgraph client for real-time galaxy data
	var memgraphClient *memgraph.Client
	if cfg.EDIN.Memgraph.Enabled {
		mgClient, err := memgraph.NewClient(memgraph.Config{
			Host:     cfg.EDIN.Memgraph.Host,
			Port:     cfg.EDIN.Memgraph.Port,
			Username: cfg.EDIN.Memgraph.Username,
			Password: cfg.EDIN.Memgraph.Password,
		})
		if err != nil {
			logger.Warn(fmt.Sprintf("Memgraph client creation failed: %v", err))
		} else if err := mgClient.Connect(ctx); err != nil {
			logger.Warn(fmt.Sprintf("Memgraph connection failed: %v", err))
		} else {
			memgraphClient = mgClient
			defer mgClient.Close(ctx)
			logger.Info("Memgraph connected for real-time galaxy data")
		}
	}

	var anthropicClient *anthropic.Client
	if cfg.Anthropic.APIKey != "" {
		client, err := anthropic.New(cfg.Anthropic.APIKey, cfg.Anthropic.Model, cfg.Anthropic.MaxTokens)
		if err != nil {
			log.Fatalf("initialise anthropic client: %v", err)
		}
		anthropicClient = client
	}

	// Create WebSocket hub for real-time updates
	wsHub := websocket.NewHub()
	go wsHub.Run()
	logger.Info("WebSocket hub started")

	// Initialize game server metrics and collector
	gsMetrics := gameservers.InitMetrics("ssg")
	gsCollector := gameservers.NewCollector(nil, gsMetrics, observability.NewLogger("gameservers"))
	gsCollector.Start(ctx)
	defer gsCollector.Stop()

	// Initialize DayZ service for map/spawn data
	dayzConfig := dayz.DefaultConfig()
	// DayZ server is on a separate host (54.37.128.230), not on ssg.sh
	// Use direct IP for A2S status queries, but disable Docker mode since
	// we can't docker exec across servers - fallback data will be used for spawns
	dayzConfig.ServerHost = "54.37.128.230"
	dayzConfig.QueryPort = 2305
	dayzConfig.UseDocker = false // Can't docker exec to remote server
	dayzService := dayz.NewService(dayzConfig, observability.NewLogger("dayz"))
	logger.Info("DayZ spawn data service initialized")

	toolExecutor := tools.NewExecutor(opsManager, spanshClient, edsmClient, cacheStore)
	toolExecutor.WithBroadcaster(wsHub)
	if memgraphClient != nil {
		toolExecutor.WithMemgraph(memgraphClient)
	}
	// Wire up history client for historical powerplay queries (uses EDDN raw database)
	if cacheStore != nil {
		toolExecutor.WithHistoryClient(cacheStore)
	}

	var assistantRunner *assistant.Runner
	if anthropicClient != nil {
		assistantRunner = assistant.NewRunner(anthropicClient, toolExecutor, cfg.LLM.SystemPrompt, cfg.LLM.MaxIterations)
	}

	go func() {
		if err := mcp.Run(ctx, cfg, opsManager, llmStore, anthropicClient, toolExecutor, assistantRunner); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("mcp server", err)
			cancel()
		}
	}()

	if err := httpapi.Run(ctx, cfg, opsManager, llmStore, anthropicClient, toolExecutor, assistantRunner, spanshClient, cacheStore, wsHub, memgraphClient, dayzService, kaineStore, eddnIntelStore); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("http api server", err)
		cancel()
	}
}

func buildConversationStore(storeCfg config.ConversationStoreConfig) (llm.SessionBackend, *redis.Client) {
	ttl := storeCfg.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	fallback := llm.NewInMemoryStore(ttl)
	fallback.SetMaxMessages(storeCfg.MaxMessages)

	if strings.EqualFold(storeCfg.Backend, "redis") && storeCfg.Redis.Enabled {
		opts := &redis.Options{
			Addr:     storeCfg.Redis.Addr,
			Username: storeCfg.Redis.Username,
			Password: storeCfg.Redis.Password,
			DB:       storeCfg.Redis.DB,
		}
		if storeCfg.Redis.TLSEnabled {
			opts.TLSConfig = &tls.Config{InsecureSkipVerify: storeCfg.Redis.TLSSkipVerify} // #nosec G402
		}

		client := redis.NewClient(opts)
		logger := observability.NewLogger("llm.store")

		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			logger.Warn(fmt.Sprintf("redis unavailable, falling back to in-memory store: %v", err))
			_ = client.Close()
			return fallback, nil
		}

		logger.Info("using redis conversation store")
		store := llm.NewRedisStore(client, ttl, storeCfg.MaxMessages, fallback, llm.WithRedisLogger(logger))
		return store, client
	}

	return fallback, nil
}
