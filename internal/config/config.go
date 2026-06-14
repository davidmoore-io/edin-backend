package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/voice"
)

// Config aggregates runtime configuration for both the control API and the Discord bot.
type Config struct {
	DomainName        string
	ControlAPIDomain  string
	ControlBotDomain  string
	PublicIPv4        string
	ControlAPIBaseURL string
	MCPBaseURL        string
	EnableMCPStdIO    bool

	HTTP          HTTPConfig
	Discord       DiscordConfig
	Anthropic     AnthropicConfig
	LLM           LLMConfig
	Operations    OperationsConfig
	RateLimit     RateLimitConfig
	Logging       LoggingConfig
	EDIN          EDINConfig
	KaineAuth     KaineAuthConfig
	Authentik     AuthentikConfig
	CommanderAuth CommanderAuthConfig
	Copilot       CopilotConfig
	ElevenLabs    ElevenLabsConfig
}

// AuthentikConfig holds Authentik identity provider API settings.
type AuthentikConfig struct {
	Enabled bool
	URL     string
	Token   string
}

// CommanderAuthConfig holds Frontier PKCE auth + EDIN JWT settings for the Commander (Copilot) feature.
type CommanderAuthConfig struct {
	Enabled              bool
	PrivateKeyPath       string        // Path to RSA-2048 PEM private key file
	PublicKeyPath        string        // Path to RSA-2048 PEM public key file
	JWTIssuer            string        // "edin-space"
	JWTExpiry            time.Duration // 24h default
	FrontierClientID     string        // Frontier OAuth2 client ID
	FrontierClientSecret string        // Frontier OAuth2 client secret
	FrontierAuthURL      string        // "https://auth.frontierstore.net"
	FrontierCAPIURL      string        // "https://companion.orerve.net"
	FrontierScope        string        // "auth capi" — MUST default to "auth capi"
	FrontierCAPITimeout  time.Duration // 10s default — CAPI is unreliable/slow
	PKCEStateTTL         time.Duration // 10m — how long PKCE state is valid
	PKCEMaxPending       int           // 1000 — max pending PKCE auth sessions
	CookieName           string        // "commander_session"
	CookiePath           string        // "/api/commander"
	CookieDomain         string        // ".edin.space" in prod, "" in dev
	CookieSecure         bool          // true in prod (Caddy adds Secure)
	CookieMaxAge         int           // 86400 (24h)
	// Nonce settings
	NonceExpiry time.Duration // 10s — single-use nonce TTL for WebSocket auth frame
	// Per-IP rate limit for the /initiate endpoint
	InitiateRateLimit  int           // 5 — requests per window per IP
	InitiateRateWindow time.Duration // 1m — window duration
	// Commander DB connections (DSN form — role-separated for RLS enforcement)
	CmdWriterDSN string // postgres DSN for writes (edin_cmd_writer role)
	CmdReaderDSN string // postgres DSN for reads  (edin_cmd_reader role)
	// Migrator DSN — runs the embedded commander schema migrations at startup.
	// Must use a role with owner privileges (CREATE SCHEMA, CREATE TABLE, create_hypertable,
	// GRANT) so realistically the TimescaleDB superuser. Leave empty to skip
	// migrations (e.g. test envs that bring their own schema).
	CmdMigratorDSN string
	// Desktop flow redirect URI — must match what's registered with Frontier
	DesktopRedirectURI string // "https://edin.space/api/commander/auth/callback"

	// LoginAttemptLogPath — file to append rejected-login JSON lines to.
	// Empty disables file logging; rejections still go to the server logger.
	LoginAttemptLogPath string
	// AdminActionsLogPath — file to append admin-action JSON lines to
	// (Task 8 Grant/Revoke/Link/Unlink/Approve/Deny). Empty disables file
	// logging; admin actions still go to the server logger.
	AdminActionsLogPath string
}

// CopilotConfig holds WebSocket tuning and AI call parameters for the Copilot chat feature.
type CopilotConfig struct {
	WSAuthTimeout       time.Duration // 5s  — wait for auth frame after upgrade
	WSReadDeadline      time.Duration // 900s — must exceed worst-case turn length (handler is synchronous; see default below)
	WSPingInterval      time.Duration // 30s — server ping interval
	WSWriteDeadline     time.Duration // 10s — write deadline for outgoing frames
	WSReadLimitBytes    int64         // 65536 (64 KB) — max incoming message size
	MessageHistoryLimit int           // 20  — messages sent to Anthropic per call
	EventsDefaultLimit  int           // 20  — default event count for commander_events tool
	EventsMaxLimit      int           // 100 — maximum event count for commander_events tool
}

// ElevenLabsConfig holds ElevenLabs TTS API settings.
// Voice is optional — the backend starts and functions without it.
type ElevenLabsConfig struct {
	APIKey               string
	PersonalityTemplateDir string
	Voices               voice.PersonaVoices
}

// KaineAuthConfig holds Kaine portal JWT authentication settings.
type KaineAuthConfig struct {
	Enabled         bool
	JWKSURL         string
	Issuer          string
	Audience        string
	RefreshInterval time.Duration

	// OAuth2 code exchange settings (Story 1.2)
	ClientID string // OAuth2 client ID for Kaine portal (e.g. "kaine-portal")
	TokenURL string // Authentik token endpoint (e.g. "https://auth.ssg.sh/application/o/token/")

	// httpOnly cookie settings (Story 1.2)
	CookieName   string // "kaine_session"
	CookiePath   string // "/api/kaine"
	CookieDomain string // ".edin.space" in prod, "" in dev
	CookieSecure bool   // true in prod, false in dev
	CookieMaxAge int    // 3600 (1 hour)

	// Bot M2M (client_credentials) trust. The edin-bot calls control-API
	// with JWTs from a separate Authentik provider that has its own issuer,
	// audience and JWKS endpoint. If any of these are set, all three must be
	// set; the JWT validator then accepts both the kaine-portal trust and
	// the edin-bot trust.
	BotIssuer   string
	BotAudience string
	BotJWKSURL  string
}

// EDINConfig holds configuration for the EDIN (Elite Dangerous Intel Network) database.
type EDINConfig struct {
	Enabled  bool
	Host     string
	Port     int
	User     string
	Password string
	Database string
	Schema   string
	PoolSize int

	// Memgraph configuration (for current state queries)
	Memgraph MemgraphConfig

	// EDDNRaw configuration (for raw EDDN feed queries - historical data)
	EDDNRaw EDDNRawConfig
}

// EDDNRawConfig holds configuration for the raw EDDN feed database.
type EDDNRawConfig struct {
	Enabled  bool
	Host     string
	Port     int
	User     string
	Password string
	Database string
	Schema   string
	PoolSize int
}

// MemgraphConfig holds Memgraph graph database connection settings.
type MemgraphConfig struct {
	Enabled  bool
	Host     string
	Port     int
	Username string
	Password string
}

// HTTPConfig captures HTTP server settings.
type HTTPConfig struct {
	Address      string
	TLSCertPath  string
	TLSKeyPath   string
	InternalKey  string
	EnableTLS    bool
	AllowOrigins []string
	MCPAddress   string
}

// DiscordConfig stores Discord application credentials and policy.
type DiscordConfig struct {
	BotToken               string
	AppID                  string
	PublicKey              string
	GuildIDs               []string
	AdminRoleIDs           []string
	LLMOperatorRoleIDs     []string
	ServiceRoleIDs         map[string][]string
	InteractionWebhookAuth string
}

// AnthropicConfig governs Anthropic API usage.
type AnthropicConfig struct {
	APIKey    string
	Model     string
	MaxTokens int
}

// LLMConfig captures assistant behaviour.
type LLMConfig struct {
	SystemPrompt      string
	KaineSystemPrompt string // Separate prompt for Kaine chat (Elite Dangerous only, no ops tools)
	MaxIterations     int
	Store             ConversationStoreConfig
}

// ConversationStoreConfig describes how conversation history is persisted.
type ConversationStoreConfig struct {
	Backend     string
	TTL         time.Duration
	MaxMessages int
	Redis       RedisStoreConfig
}

// RedisStoreConfig captures Redis connection settings.
type RedisStoreConfig struct {
	Enabled       bool
	Addr          string
	Username      string
	Password      string
	DB            int
	TLSEnabled    bool
	TLSSkipVerify bool
}

// OperationsConfig describes allowed Docker containers, Ansible playbooks, and log defaults.
type OperationsConfig struct {
	DockerBinary  string
	Services      map[string]DockerService
	ServiceLabels map[string]string

	AnsibleBinary    string
	AnsibleInventory string
	Playbooks        map[string]string

	LogTailDefault int
}

// DockerService describes a managed container.
type DockerService struct {
	Container string
}

// RateLimitConfig defines request rate limiting.
type RateLimitConfig struct {
	RequestsPerWindow int
	Window            time.Duration
}

// LoggingConfig controls logging behaviour.
type LoggingConfig struct {
	JSON bool
}

// Load reads configuration from environment variables and returns a populated Config.
func Load() (*Config, error) {
	domain, err := requiredEnv("SSG_DOMAIN")
	if err != nil {
		return nil, err
	}

	controlAPIDomain, err := requiredEnv("SSG_CONTROL_API_DOMAIN")
	if err != nil {
		return nil, err
	}

	controlBotDomain, err := requiredEnv("SSG_CONTROL_BOT_DOMAIN")
	if err != nil {
		return nil, err
	}

	publicIPv4 := os.Getenv("SSG_PUBLIC_IPV4")
	controlAPIBaseURL := getenvDefault("SSG_CONTROL_API_BASE_URL", fmt.Sprintf("https://%s", controlAPIDomain))
	mcpBaseURL := getenvDefault("SSG_MCP_BASE_URL", fmt.Sprintf("%s/mcp", strings.TrimRight(controlAPIBaseURL, "/")))
	enableStdIO := getEnvBool("MCP_STDIO_ENABLE", false)

	httpCfg, err := loadHTTPConfig()
	if err != nil {
		return nil, err
	}

	discordCfg, err := loadDiscordConfig()
	if err != nil {
		return nil, err
	}

	anthropicCfg, err := loadAnthropicConfig()
	if err != nil {
		return nil, err
	}

	llmCfg := loadLLMConfig()

	opsCfg, err := loadOperationsConfig()
	if err != nil {
		return nil, err
	}

	rateCfg := loadRateLimitConfig()
	logCfg := loadLoggingConfig()
	edinCfg := loadEDINConfig()
	kaineAuthCfg := loadKaineAuthConfig()
	authentikCfg := loadAuthentikConfig()
	commanderAuthCfg := loadCommanderAuthConfig()
	copilotCfg := loadCopilotConfig()

	var elevenLabsCfg ElevenLabsConfig
	elCfg, err := loadElevenLabsConfig()
	if err != nil {
		// Voice is optional — backend starts and functions without it.
		log.Printf("WARNING: ElevenLabs not configured (%v) — voice disabled", err)
	} else {
		elevenLabsCfg = elCfg
	}

	return &Config{
		DomainName:        domain,
		ControlAPIDomain:  controlAPIDomain,
		ControlBotDomain:  controlBotDomain,
		PublicIPv4:        publicIPv4,
		ControlAPIBaseURL: controlAPIBaseURL,
		MCPBaseURL:        mcpBaseURL,
		EnableMCPStdIO:    enableStdIO,
		HTTP:              httpCfg,
		Discord:           discordCfg,
		Anthropic:         anthropicCfg,
		LLM:               llmCfg,
		Operations:        opsCfg,
		RateLimit:         rateCfg,
		Logging:           logCfg,
		EDIN:              edinCfg,
		KaineAuth:         kaineAuthCfg,
		Authentik:         authentikCfg,
		CommanderAuth:     commanderAuthCfg,
		Copilot:           copilotCfg,
		ElevenLabs:        elevenLabsCfg,
	}, nil
}

func loadHTTPConfig() (HTTPConfig, error) {
	address := getenvDefault("SSG_HTTP_ADDR", ":8080")
	tlsCert := os.Getenv("SSG_HTTP_TLS_CERT")
	tlsKey := os.Getenv("SSG_HTTP_TLS_KEY")
	internalKey, err := requiredEnv("SSG_HTTP_API_KEY")
	if err != nil {
		return HTTPConfig{}, err
	}

	origins := parseCSV(os.Getenv("SSG_HTTP_ALLOW_ORIGINS"))
	enableTLS := tlsCert != "" && tlsKey != ""
	mcpAddress := getenvDefault("SSG_MCP_ADDR", ":8081")

	return HTTPConfig{
		Address:      address,
		TLSCertPath:  tlsCert,
		TLSKeyPath:   tlsKey,
		InternalKey:  internalKey,
		EnableTLS:    enableTLS,
		AllowOrigins: origins,
		MCPAddress:   mcpAddress,
	}, nil
}

func loadDiscordConfig() (DiscordConfig, error) {
	token, err := requiredEnv("DISCORD_BOT_TOKEN")
	if err != nil {
		return DiscordConfig{}, err
	}
	appID, err := requiredEnv("DISCORD_APP_ID")
	if err != nil {
		return DiscordConfig{}, err
	}
	publicKey, err := requiredEnv("DISCORD_PUBLIC_KEY")
	if err != nil {
		return DiscordConfig{}, err
	}

	guilds := parseCSV(os.Getenv("DISCORD_GUILD_IDS"))
	adminRoles := parseCSV(os.Getenv("DISCORD_ADMIN_ROLE_IDS"))
	llmRoles := parseCSV(os.Getenv("DISCORD_LLM_ROLE_IDS"))
	serviceRoles, err := parseStringListMap(os.Getenv("DISCORD_SERVICE_ROLE_IDS"))
	if err != nil {
		return DiscordConfig{}, err
	}
	webhookSecret := os.Getenv("DISCORD_INTERACTION_SECRET")

	return DiscordConfig{
		BotToken:               token,
		AppID:                  appID,
		PublicKey:              publicKey,
		GuildIDs:               guilds,
		AdminRoleIDs:           adminRoles,
		LLMOperatorRoleIDs:     llmRoles,
		ServiceRoleIDs:         serviceRoles,
		InteractionWebhookAuth: webhookSecret,
	}, nil
}

func loadAnthropicConfig() (AnthropicConfig, error) {
	apiKey, err := requiredEnv("ANTHROPIC_API_KEY")
	if err != nil {
		return AnthropicConfig{}, err
	}

	model := getenvDefault("ANTHROPIC_MODEL", "claude-opus-4-6")
	maxTokens := getenvInt("ANTHROPIC_MAX_TOKENS", 16384)

	return AnthropicConfig{
		APIKey:    apiKey,
		Model:     model,
		MaxTokens: maxTokens,
	}, nil
}

func loadLLMConfig() LLMConfig {
	rawPrompt := getenvDefault("LLM_SYSTEM_PROMPT", defaultSystemPrompt())
	rawPrompt = strings.ReplaceAll(rawPrompt, `\n`, "\n")
	prompt := strings.TrimSpace(rawPrompt)

	// Kaine chat uses a separate prompt with only Elite Dangerous tools (no ops)
	rawKainePrompt := getenvDefault("LLM_KAINE_SYSTEM_PROMPT", defaultKaineSystemPrompt())
	rawKainePrompt = strings.ReplaceAll(rawKainePrompt, `\n`, "\n")
	kainePrompt := strings.TrimSpace(rawKainePrompt)

	maxIterations := getenvInt("LLM_MAX_TOOL_ITERATIONS", 5)
	if maxIterations <= 0 {
		maxIterations = 5
	}
	return LLMConfig{
		SystemPrompt:      prompt,
		KaineSystemPrompt: kainePrompt,
		MaxIterations:     maxIterations,
		Store:             loadConversationStoreConfig(),
	}
}

func loadConversationStoreConfig() ConversationStoreConfig {
	const (
		defaultBackend     = "memory"
		defaultTTL         = 30 * time.Minute
		defaultMaxMessages = 20
	)

	rawBackend := firstNonEmpty(
		os.Getenv("CONVO_HISTORY_BACKEND"),
		os.Getenv("LLM_STORE_BACKEND"),
	)
	backend := strings.ToLower(strings.TrimSpace(rawBackend))
	if backend == "" {
		backend = defaultBackend
	}

	ttl := getEnvDurationAny([]string{"CONVO_HISTORY_TTL", "LLM_STORE_TTL"}, defaultTTL)
	if ttl <= 0 {
		ttl = defaultTTL
	}

	maxMessages := getenvIntAny([]string{"CONVO_HISTORY_MAX_MESSAGES", "LLM_STORE_MAX_MESSAGES"}, defaultMaxMessages)
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}

	redisEnabled := boolAny(
		backend == "redis",
		getEnvBool("REDIS_ENABLED", false),
		getEnvBool("CONVO_HISTORY_REDIS_ENABLED", false),
		getEnvBool("LLM_REDIS_ENABLED", false),
	)
	if redisEnabled {
		backend = "redis"
	}

	redisAddr := firstNonEmpty(
		os.Getenv("CONVO_HISTORY_REDIS_ADDR"),
		os.Getenv("REDIS_ADDR"),
		os.Getenv("LLM_REDIS_ADDR"),
		"127.0.0.1:6379",
	)
	redisUser := firstNonEmpty(
		os.Getenv("CONVO_HISTORY_REDIS_USERNAME"),
		os.Getenv("REDIS_USERNAME"),
		os.Getenv("LLM_REDIS_USERNAME"),
	)
	redisPass := firstNonEmpty(
		os.Getenv("CONVO_HISTORY_REDIS_PASSWORD"),
		os.Getenv("REDIS_PASSWORD"),
		os.Getenv("LLM_REDIS_PASSWORD"),
	)
	redisDB := getenvIntAny([]string{"CONVO_HISTORY_REDIS_DB", "REDIS_DB", "LLM_REDIS_DB"}, 0)
	redisTLS := boolAny(
		getEnvBool("CONVO_HISTORY_REDIS_TLS_ENABLED", false),
		getEnvBool("REDIS_TLS_ENABLED", false),
		getEnvBool("LLM_REDIS_TLS", false),
	)
	redisSkipVerify := boolAny(
		getEnvBool("CONVO_HISTORY_REDIS_TLS_SKIP_VERIFY", false),
		getEnvBool("REDIS_TLS_SKIP_VERIFY", false),
		getEnvBool("LLM_REDIS_TLS_SKIP_VERIFY", false),
	)

	return ConversationStoreConfig{
		Backend:     backend,
		TTL:         ttl,
		MaxMessages: maxMessages,
		Redis: RedisStoreConfig{
			Enabled:       redisEnabled,
			Addr:          redisAddr,
			Username:      redisUser,
			Password:      redisPass,
			DB:            redisDB,
			TLSEnabled:    redisTLS,
			TLSSkipVerify: redisSkipVerify,
		},
	}
}

func loadOperationsConfig() (OperationsConfig, error) {
	dockerBinary := getenvDefault("DOCKER_BINARY", "docker")
	services, err := parseServiceMap(os.Getenv("DOCKER_SERVICES"))
	if err != nil {
		return OperationsConfig{}, err
	}
	if len(services) == 0 {
		return OperationsConfig{}, errors.New("DOCKER_SERVICES must describe at least one service")
	}

	labels, err := parseKeyValueMap(os.Getenv("DOCKER_SERVICE_LABELS"))
	if err != nil {
		return OperationsConfig{}, err
	}

	ansibleBinary := getenvDefault("ANSIBLE_PLAYBOOK_BIN", "ansible-playbook")
	playbooks, err := parseKeyValueMap(os.Getenv("ANSIBLE_PLAYBOOKS"))
	if err != nil {
		return OperationsConfig{}, err
	}

	inventory := os.Getenv("ANSIBLE_INVENTORY")
	logTailDefault := getenvInt("LOG_TAIL_DEFAULT", 20)

	return OperationsConfig{
		DockerBinary:     dockerBinary,
		Services:         services,
		ServiceLabels:    labels,
		AnsibleBinary:    ansibleBinary,
		AnsibleInventory: inventory,
		Playbooks:        playbooks,
		LogTailDefault:   logTailDefault,
	}, nil
}

func loadRateLimitConfig() RateLimitConfig {
	reqs := getenvInt("RATE_LIMIT_REQUESTS", 60)
	windowStr := getenvDefault("RATE_LIMIT_WINDOW", "1m")
	window, err := time.ParseDuration(windowStr)
	if err != nil {
		window = time.Minute
	}
	return RateLimitConfig{
		RequestsPerWindow: reqs,
		Window:            window,
	}
}

func loadLoggingConfig() LoggingConfig {
	json := strings.EqualFold(os.Getenv("SSG_LOG_JSON"), "true")
	return LoggingConfig{JSON: json}
}

func loadKaineAuthConfig() KaineAuthConfig {
	refreshStr := getenvDefault("KAINE_AUTH_JWKS_REFRESH", "1h")
	refresh, err := time.ParseDuration(refreshStr)
	if err != nil {
		refresh = time.Hour
	}

	return KaineAuthConfig{
		Enabled:         getEnvBool("KAINE_AUTH_ENABLED", true),
		JWKSURL:         getenvDefault("KAINE_AUTH_JWKS_URL", "https://auth.ssg.sh/application/o/kaine-portal/jwks/"),
		Issuer:          getenvDefault("KAINE_AUTH_ISSUER", "https://auth.ssg.sh/application/o/kaine-portal/"),
		Audience:        getenvDefault("KAINE_AUTH_AUDIENCE", "kaine-portal"),
		RefreshInterval: refresh,
		ClientID:        getenvDefault("KAINE_AUTH_CLIENT_ID", "kaine-portal"),
		TokenURL:        getenvDefault("KAINE_AUTH_TOKEN_URL", "https://auth.ssg.sh/application/o/token/"),
		CookieName:      "kaine_session",
		CookiePath:      "/api/kaine",
		CookieDomain:    os.Getenv("KAINE_AUTH_COOKIE_DOMAIN"),
		CookieSecure:    getEnvBool("KAINE_AUTH_COOKIE_SECURE", false),
		CookieMaxAge:    3600,

		BotIssuer:   os.Getenv("BOT_AUTH_ISSUER"),
		BotAudience: os.Getenv("BOT_AUTH_AUDIENCE"),
		BotJWKSURL:  os.Getenv("BOT_AUTH_JWKS_URL"),
	}
}

func loadAuthentikConfig() AuthentikConfig {
	return AuthentikConfig{
		Enabled: getEnvBool("AUTHENTIK_API_ENABLED", false),
		URL:     getenvDefault("AUTHENTIK_API_URL", "https://auth.ssg.sh"),
		Token:   os.Getenv("AUTHENTIK_API_TOKEN"),
	}
}

func loadCommanderAuthConfig() CommanderAuthConfig {
	privateKeyPath := os.Getenv("COMMANDER_JWT_PRIVATE_KEY_PATH")
	publicKeyPath := os.Getenv("COMMANDER_JWT_PUBLIC_KEY_PATH")

	// Enabled is false if either key path is empty, regardless of the env var.
	enabled := getEnvBool("COMMANDER_AUTH_ENABLED", false) && privateKeyPath != "" && publicKeyPath != ""

	jwtExpiry := getEnvDuration("COMMANDER_JWT_EXPIRY", 24*time.Hour)
	if jwtExpiry <= 0 {
		jwtExpiry = 24 * time.Hour
	}

	capiTimeout := getEnvDuration("FRONTIER_CAPI_TIMEOUT", 10*time.Second)
	if capiTimeout <= 0 {
		capiTimeout = 10 * time.Second
	}

	pkceStateTTL := getEnvDuration("PKCE_STATE_TTL", 10*time.Minute)
	if pkceStateTTL <= 0 {
		pkceStateTTL = 10 * time.Minute
	}

	pkceMaxPending := getenvInt("PKCE_MAX_PENDING", 1000)
	if pkceMaxPending <= 0 {
		pkceMaxPending = 1000
	}

	nonceExpiry := getEnvDuration("COMMANDER_NONCE_EXPIRY", 10*time.Second)
	if nonceExpiry <= 0 {
		nonceExpiry = 10 * time.Second
	}

	initiateRateLimit := getenvInt("COMMANDER_AUTH_INITIATE_RATE_LIMIT", 5)
	if initiateRateLimit <= 0 {
		initiateRateLimit = 5
	}

	initiateRateWindow := getEnvDuration("COMMANDER_AUTH_INITIATE_RATE_WINDOW", time.Minute)
	if initiateRateWindow <= 0 {
		initiateRateWindow = time.Minute
	}

	return CommanderAuthConfig{
		Enabled:              enabled,
		PrivateKeyPath:       privateKeyPath,
		PublicKeyPath:        publicKeyPath,
		JWTIssuer:            getenvDefault("COMMANDER_JWT_ISSUER", "edin-space"),
		JWTExpiry:            jwtExpiry,
		FrontierClientID:     os.Getenv("FRONTIER_CLIENT_ID"),
		FrontierClientSecret: os.Getenv("FRONTIER_CLIENT_SECRET"),
		FrontierAuthURL:      getenvDefault("FRONTIER_AUTH_URL", "https://auth.frontierstore.net"),
		FrontierCAPIURL:      getenvDefault("FRONTIER_CAPI_URL", "https://companion.orerve.net"),
		FrontierScope:        getenvDefault("FRONTIER_SCOPE", "auth capi"),
		FrontierCAPITimeout:  capiTimeout,
		PKCEStateTTL:         pkceStateTTL,
		PKCEMaxPending:       pkceMaxPending,
		CookieName:           "commander_session",
		CookiePath:           "/api/commander",
		CookieDomain:         os.Getenv("COMMANDER_COOKIE_DOMAIN"),
		CookieSecure:         getEnvBool("COMMANDER_COOKIE_SECURE", false),
		CookieMaxAge:         86400,
		NonceExpiry:          nonceExpiry,
		InitiateRateLimit:    initiateRateLimit,
		InitiateRateWindow:   initiateRateWindow,
		CmdWriterDSN:         os.Getenv("EDIN_CMD_WRITER_DSN"),
		CmdReaderDSN:         os.Getenv("EDIN_CMD_READER_DSN"),
		CmdMigratorDSN:       os.Getenv("EDIN_CMD_MIGRATOR_DSN"),
		DesktopRedirectURI:   getenvDefault("DESKTOP_REDIRECT_URI", "https://edin.space/api/commander/auth/callback"),
		LoginAttemptLogPath:  os.Getenv("COMMANDER_LOGIN_ATTEMPT_LOG"),
		AdminActionsLogPath:  os.Getenv("COMMANDER_ADMIN_ACTIONS_LOG"),
	}
}

func loadCopilotConfig() CopilotConfig {
	wsReadLimit := int64(getenvInt("COPILOT_WS_READ_LIMIT_BYTES", 65536))
	if wsReadLimit <= 0 {
		wsReadLimit = 65536
	}
	eventsDefault := getenvInt("COPILOT_EVENTS_DEFAULT_LIMIT", 20)
	if eventsDefault <= 0 {
		eventsDefault = 20
	}
	eventsMax := getenvInt("COPILOT_EVENTS_MAX_LIMIT", 100)
	if eventsMax <= 0 {
		eventsMax = 100
	}
	return CopilotConfig{
		WSAuthTimeout:       getEnvDuration("COPILOT_WS_AUTH_TIMEOUT", 5*time.Second),
		// 15 min: handleCopilotMessage runs synchronously in the read loop, so the
		// read deadline is not extended (via pong) while a turn is in flight. It must
		// therefore exceed the worst-case turn duration, or the socket is closed the
		// instant a long multi-tool turn completes. Between turns, the 30s ping/pong
		// keeps the deadline fresh, so a large value does not delay dead-peer cleanup
		// in practice.
		WSReadDeadline:      getEnvDuration("COPILOT_WS_READ_DEADLINE", 900*time.Second),
		WSPingInterval:      getEnvDuration("COPILOT_WS_PING_INTERVAL", 30*time.Second),
		WSWriteDeadline:     getEnvDuration("COPILOT_WS_WRITE_DEADLINE", 10*time.Second),
		WSReadLimitBytes:    wsReadLimit,
		MessageHistoryLimit: getenvInt("COPILOT_MESSAGE_HISTORY_LIMIT", 20),
		EventsDefaultLimit:  eventsDefault,
		EventsMaxLimit:      eventsMax,
	}
}

func loadElevenLabsConfig() (ElevenLabsConfig, error) {
	apiKey, err := requiredEnv("ELEVENLABS_API_KEY")
	if err != nil {
		return ElevenLabsConfig{}, err
	}
	return ElevenLabsConfig{
		APIKey:               apiKey,
		PersonalityTemplateDir: getenvDefault("PERSONALITY_TEMPLATE_DIR", "../edin-personality/system-prompts"),
		Voices:               voice.LoadPersonaVoices(),
	}, nil
}

func loadEDINConfig() EDINConfig {
	enabled := getEnvBool("EDIN_DB_ENABLED", false)
	host := getenvDefault("EDIN_DB_HOST", "10.8.0.3") // db.ssg.sh via WireGuard

	return EDINConfig{
		Enabled:  enabled,
		Host:     host,
		Port:     getenvInt("EDIN_DB_PORT", 5432),
		User:     getenvDefault("EDIN_DB_USER", "edin_writer"),
		Password: os.Getenv("EDIN_DB_PASSWORD"),
		Database: getenvDefault("EDIN_DB_NAME", "edin"),
		Schema:   getenvDefault("EDIN_DB_SCHEMA", "powerplay"),
		PoolSize: getenvInt("EDIN_DB_POOL_SIZE", 5),
		Memgraph: MemgraphConfig{
			Enabled:  getEnvBool("MEMGRAPH_ENABLED", false),
			Host:     getenvDefault("MEMGRAPH_HOST", "10.8.0.3"), // db.ssg.sh via WireGuard
			Port:     getenvInt("MEMGRAPH_PORT", 7687),
			Username: getenvDefault("MEMGRAPH_USERNAME", ""),
			Password: os.Getenv("MEMGRAPH_PASSWORD"),
		},
		EDDNRaw: EDDNRawConfig{
			Enabled:  getEnvBool("EDDN_RAW_DB_ENABLED", enabled), // Default to same as EDIN
			Host:     getenvDefault("EDDN_RAW_DB_HOST", host),    // Same host as EDIN
			Port:     getenvInt("EDDN_RAW_DB_PORT", 5433),        // Different port
			User:     getenvDefault("EDDN_RAW_DB_USER", "eddn_admin"),
			Password: os.Getenv("EDDN_RAW_DB_PASSWORD"),
			Database: getenvDefault("EDDN_RAW_DB_NAME", "eddn_raw"),
			Schema:   getenvDefault("EDDN_RAW_DB_SCHEMA", "feed"),
			PoolSize: getenvInt("EDDN_RAW_DB_POOL_SIZE", 3),
		},
	}
}

func requiredEnv(key string) (string, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return "", fmt.Errorf("%s not set", key)
	}
	return val, nil
}

func getenvDefault(key, def string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	return val
}

func getenvInt(key string, def int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return def
	}
	return n
}

func parseCSV(val string) []string {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trim := strings.TrimSpace(p)
		if trim != "" {
			out = append(out, trim)
		}
	}
	return out
}

func parseServiceMap(raw string) (map[string]DockerService, error) {
	result := make(map[string]DockerService)
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	pairs := strings.Split(raw, ",")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		split := strings.SplitN(p, ":", 2)
		if len(split) != 2 {
			return nil, fmt.Errorf("invalid DOCKER_SERVICES entry: %s", p)
		}
		name := strings.TrimSpace(split[0])
		container := strings.TrimSpace(split[1])
		if name == "" || container == "" {
			return nil, fmt.Errorf("invalid DOCKER_SERVICES entry: %s", p)
		}
		result[name] = DockerService{Container: container}
	}
	return result, nil
}

func parseKeyValueMap(raw string) (map[string]string, error) {
	result := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	pairs := strings.Split(raw, ",")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		split := strings.SplitN(p, "=", 2)
		if len(split) != 2 {
			return nil, fmt.Errorf("invalid key=value entry: %s", p)
		}
		key := strings.TrimSpace(split[0])
		val := strings.TrimSpace(split[1])
		if key == "" || val == "" {
			return nil, fmt.Errorf("invalid key=value entry: %s", p)
		}
		result[key] = val
	}
	return result, nil
}

func parseStringListMap(raw string) (map[string][]string, error) {
	result := make(map[string][]string)
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	pairs := strings.Split(raw, ",")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		split := strings.SplitN(p, "=", 2)
		if len(split) != 2 {
			return nil, fmt.Errorf("invalid key=list entry: %s", p)
		}
		key := strings.TrimSpace(split[0])
		valuesRaw := strings.TrimSpace(split[1])
		if key == "" || valuesRaw == "" {
			continue
		}
		values := strings.Split(valuesRaw, "|")
		cleaned := make([]string, 0, len(values))
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v != "" {
				cleaned = append(cleaned, v)
			}
		}
		if len(cleaned) == 0 {
			continue
		}
		result[key] = cleaned
	}
	return result, nil
}

func getEnvBool(key string, def bool) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	dur, err := time.ParseDuration(val)
	if err != nil {
		return def
	}
	return dur
}

func getEnvDurationAny(keys []string, def time.Duration) time.Duration {
	for _, key := range keys {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			if dur, err := time.ParseDuration(val); err == nil {
				return dur
			}
		}
	}
	return def
}

func getenvIntAny(keys []string, def int) int {
	for _, key := range keys {
		if val, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
			return val
		}
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func boolAny(values ...bool) bool {
	for _, v := range values {
		if v {
			return true
		}
	}
	return false
}
