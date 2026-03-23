package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

	HTTP       HTTPConfig
	Discord    DiscordConfig
	Anthropic  AnthropicConfig
	LLM        LLMConfig
	Operations OperationsConfig
	RateLimit  RateLimitConfig
	Logging    LoggingConfig
	EDIN       EDINConfig
	KaineAuth  KaineAuthConfig
	Authentik  AuthentikConfig
}

// AuthentikConfig holds Authentik identity provider API settings.
type AuthentikConfig struct {
	Enabled bool
	URL     string
	Token   string
}

// KaineAuthConfig holds Kaine portal JWT authentication settings.
type KaineAuthConfig struct {
	Enabled         bool
	JWKSURL         string
	Issuer          string
	Audience        string
	RefreshInterval time.Duration
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
	}
}

func loadAuthentikConfig() AuthentikConfig {
	return AuthentikConfig{
		Enabled: getEnvBool("AUTHENTIK_API_ENABLED", false),
		URL:     getenvDefault("AUTHENTIK_API_URL", "https://auth.ssg.sh"),
		Token:   os.Getenv("AUTHENTIK_API_TOKEN"),
	}
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
