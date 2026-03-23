package dayz

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/observability"
)

// Config holds DayZ service configuration.
type Config struct {
	// ServerHost is the DayZ server IP for A2S queries.
	ServerHost string
	// QueryPort is the Steam query port (usually game port + 3).
	QueryPort int
	// ContainerName is the Docker container name for exec commands.
	ContainerName string
	// MissionPath is the path inside the container to mission files.
	MissionPath string
	// CacheTTL is how long to cache spawn data.
	CacheTTL time.Duration
	// UseDocker determines if we use docker exec (true) or direct file access (false).
	UseDocker bool
}

// DefaultConfig returns sensible defaults for the SSG DayZ server.
func DefaultConfig() Config {
	return Config{
		ServerHost:    "127.0.0.1", // localhost when running on same host
		QueryPort:     2305,
		ContainerName: "dayz-server",
		MissionPath:   "/dayz/DayZServer/mpmissions",
		CacheTTL:      5 * time.Minute,
		UseDocker:     true,
	}
}

// Service provides DayZ server data querying and caching.
type Service struct {
	config Config
	logger *observability.Logger

	mu           sync.RWMutex
	cachedSpawns *MapSpawnData
	cachedStatus *ServerStatus
	lastQuery    time.Time
}

// NewService creates a new DayZ service.
func NewService(cfg Config, logger *observability.Logger) *Service {
	if logger == nil {
		logger = observability.NewLogger("dayz")
	}
	return &Service{
		config: cfg,
		logger: logger,
	}
}

// GetServerStatus queries the DayZ server for current status.
func (s *Service) GetServerStatus(ctx context.Context) (*ServerStatus, error) {
	info, err := s.queryA2SInfo(ctx)
	if err != nil {
		return &ServerStatus{Online: false}, err
	}

	status := &ServerStatus{
		Online:     true,
		Name:       info.Name,
		Map:        info.Map,
		Players:    info.Players,
		MaxPlayers: info.MaxPlayers,
		Version:    info.Version,
	}

	s.mu.Lock()
	s.cachedStatus = status
	s.mu.Unlock()

	return status, nil
}

// GetSpawnData returns spawn data for the current map, using cache if available.
func (s *Service) GetSpawnData(ctx context.Context) (*MapSpawnData, error) {
	s.mu.RLock()
	cached := s.cachedSpawns
	s.mu.RUnlock()

	// Return cached data if still valid
	if cached != nil && time.Now().Before(cached.CachedUntil) {
		return cached, nil
	}

	// Need to fetch fresh data
	return s.refreshSpawnData(ctx)
}

// GetSpawnDataForMap returns spawn data for a specific map.
func (s *Service) GetSpawnDataForMap(ctx context.Context, mapName string) (*MapSpawnData, error) {
	s.mu.RLock()
	cached := s.cachedSpawns
	s.mu.RUnlock()

	// Check if cached data matches requested map
	if cached != nil && cached.MapName == mapName && time.Now().Before(cached.CachedUntil) {
		return cached, nil
	}

	// Fetch from specific map
	return s.fetchSpawnDataForMap(ctx, mapName)
}

// refreshSpawnData fetches fresh spawn data from the server.
func (s *Service) refreshSpawnData(ctx context.Context) (*MapSpawnData, error) {
	// First, get current map from server status
	status, err := s.GetServerStatus(ctx)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to get server status: %v, using default map", err))
		// Fall back to sakhal
		return s.fetchSpawnDataForMap(ctx, "dayzOffline.sakhal")
	}

	return s.fetchSpawnDataForMap(ctx, status.Map)
}

// fetchSpawnDataForMap reads spawn config from the server for a specific map.
// Falls back to embedded data if server data cannot be retrieved.
func (s *Service) fetchSpawnDataForMap(ctx context.Context, mapName string) (*MapSpawnData, error) {
	// Extract short map name for config lookup
	shortMapName := mapName
	if strings.HasPrefix(mapName, "dayzOffline.") {
		shortMapName = mapName[12:]
	}

	// Build path to spawn config
	spawnFile := fmt.Sprintf("%s/%s/cfgeventspawns.xml", s.config.MissionPath, mapName)

	var xmlContent []byte
	var err error

	if s.config.UseDocker {
		xmlContent, err = s.readFileFromContainer(ctx, spawnFile)
	} else {
		xmlContent, err = s.readFileDirectly(ctx, spawnFile)
	}

	if err != nil {
		// Try fallback data for known maps
		s.logger.Warn(fmt.Sprintf("failed to read spawn config from server: %v, trying fallback data", err))
		fallbackData, ok := GetFallbackSpawnData(shortMapName)
		if ok {
			s.logger.Info(fmt.Sprintf("using fallback spawn data for %s: %d events, %d total points",
				shortMapName, len(fallbackData.Events), fallbackData.TotalPoints))

			// Update cache with fallback
			s.mu.Lock()
			s.cachedSpawns = fallbackData
			s.lastQuery = time.Now()
			s.mu.Unlock()

			return fallbackData, nil
		}
		return nil, fmt.Errorf("read spawn config: %w", err)
	}

	// Parse the XML
	data, err := ParseEventSpawns(xmlContent)
	if err != nil {
		return nil, fmt.Errorf("parse spawn config: %w", err)
	}

	data.MapName = shortMapName
	data.FetchedAt = time.Now()
	data.CachedUntil = time.Now().Add(s.config.CacheTTL)

	// Update cache
	s.mu.Lock()
	s.cachedSpawns = data
	s.lastQuery = time.Now()
	s.mu.Unlock()

	s.logger.Info(fmt.Sprintf("fetched spawn data for %s: %d events, %d total points",
		shortMapName, len(data.Events), data.TotalPoints))

	return data, nil
}

// readFileFromContainer reads a file from inside the Docker container.
func (s *Service) readFileFromContainer(ctx context.Context, filePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", s.config.ContainerName, "cat", filePath)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("docker exec failed: %s", string(exitErr.Stderr))
		}
		return nil, err
	}
	return output, nil
}

// readFileDirectly reads a file from the filesystem.
func (s *Service) readFileDirectly(ctx context.Context, filePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "cat", filePath)
	return cmd.Output()
}

// GetMapConfig returns the map configuration for the current or specified map.
func (s *Service) GetMapConfig(mapName string) (MapConfig, bool) {
	return GetMapConfig(mapName)
}

// GetCurrentMapConfig returns the map config for the currently running map.
func (s *Service) GetCurrentMapConfig(ctx context.Context) (MapConfig, error) {
	status, err := s.GetServerStatus(ctx)
	if err != nil {
		// Default to sakhal
		cfg, _ := GetMapConfig("sakhal")
		return cfg, nil
	}

	cfg, ok := GetMapConfig(status.Map)
	if !ok {
		return MapConfig{}, fmt.Errorf("unknown map: %s", status.Map)
	}

	return cfg, nil
}

// A2SInfo holds parsed A2S_INFO response data.
type A2SInfo struct {
	Name       string
	Map        string
	Players    int
	MaxPlayers int
	Version    string
}

// queryA2SInfo performs a Steam A2S_INFO query with challenge handling.
func (s *Service) queryA2SInfo(ctx context.Context) (*A2SInfo, error) {
	addr := net.JoinHostPort(s.config.ServerHost, strconv.Itoa(s.config.QueryPort))

	conn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	conn.SetDeadline(deadline)

	// A2S_INFO request packet
	a2sRequest := []byte{
		0xFF, 0xFF, 0xFF, 0xFF, 0x54,
		0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20,
		0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20,
		0x51, 0x75, 0x65, 0x72, 0x79, 0x00,
	}

	if _, err := conn.Write(a2sRequest); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if n < 5 {
		return nil, fmt.Errorf("response too short: %d bytes", n)
	}

	responseType := buf[4]

	// Handle challenge response (0x41 = 'A')
	if responseType == 0x41 {
		if n < 9 {
			return nil, fmt.Errorf("challenge response too short")
		}
		challenge := buf[5:9]
		requestWithChallenge := append(a2sRequest, challenge...)

		if _, err := conn.Write(requestWithChallenge); err != nil {
			return nil, fmt.Errorf("write challenge: %w", err)
		}

		n, err = conn.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("read after challenge: %w", err)
		}

		if n < 5 {
			return nil, fmt.Errorf("response after challenge too short")
		}
		responseType = buf[4]
	}

	// Parse A2S_INFO response (0x49 = 'I')
	if responseType != 0x49 {
		return nil, fmt.Errorf("unexpected response type: 0x%02X", responseType)
	}

	return s.parseA2SInfo(buf[:n])
}

func (s *Service) parseA2SInfo(data []byte) (*A2SInfo, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("data too short for A2S_INFO")
	}

	idx := 6

	readString := func() (string, error) {
		start := idx
		for idx < len(data) && data[idx] != 0 {
			idx++
		}
		if idx >= len(data) {
			return "", fmt.Errorf("string not terminated")
		}
		str := string(data[start:idx])
		idx++
		return str, nil
	}

	info := &A2SInfo{}

	var err error
	info.Name, err = readString()
	if err != nil {
		return nil, fmt.Errorf("parse name: %w", err)
	}

	info.Map, err = readString()
	if err != nil {
		return nil, fmt.Errorf("parse map: %w", err)
	}

	// Skip folder and game strings
	if _, err := readString(); err != nil {
		return nil, fmt.Errorf("parse folder: %w", err)
	}
	if _, err := readString(); err != nil {
		return nil, fmt.Errorf("parse game: %w", err)
	}

	// Skip Steam App ID (2 bytes)
	if idx+2 > len(data) {
		return nil, fmt.Errorf("data too short for app id")
	}
	idx += 2

	// Read players and max players
	if idx+2 > len(data) {
		return nil, fmt.Errorf("data too short for player counts")
	}
	info.Players = int(data[idx])
	info.MaxPlayers = int(data[idx+1])
	idx += 2

	// Skip bots, server type, environment, visibility, VAC
	idx += 5

	// Read version string
	if idx < len(data) {
		info.Version, _ = readString()
	}

	return info, nil
}

// InvalidateCache forces a cache refresh on next request.
func (s *Service) InvalidateCache() {
	s.mu.Lock()
	s.cachedSpawns = nil
	s.cachedStatus = nil
	s.mu.Unlock()
}
