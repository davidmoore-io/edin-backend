package gameservers

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/observability"
)

// ServerConfig defines a game server to monitor.
type ServerConfig struct {
	Name       string // Short identifier (e.g., "dayz", "factorio")
	Type       string // Server type for labeling
	Host       string // IP address or hostname
	Port       int    // Game port
	QueryPort  int    // Query port (0 = use Port)
	StatusPort int    // TCP status port for fallback check
	MaxPlayers int    // Expected max players (fallback)
	UseA2S     bool   // Use Steam A2S_INFO protocol
}

// ServerStatus represents the result of querying a server.
type ServerStatus struct {
	Name       string
	Type       string
	Online     bool
	Players    int
	MaxPlayers int
	QueryTime  time.Duration
	Error      error
}

// Collector periodically queries game servers and updates Prometheus metrics.
type Collector struct {
	servers  []ServerConfig
	metrics  *Metrics
	logger   *observability.Logger
	interval time.Duration

	mu       sync.RWMutex
	statuses map[string]*ServerStatus
	cancel   context.CancelFunc
}

// DefaultServers returns the standard SSG game server configuration.
// Note: All servers on ssg.sh (54.37.128.230) must use public IP since
// control-api runs in a container and 127.0.0.1 would be container loopback.
func DefaultServers() []ServerConfig {
	return []ServerConfig{
		{
			Name:       "dayz",
			Type:       "dayz",
			Host:       "54.37.128.230", // ssg.sh public IP
			Port:       2302,
			QueryPort:  2305,
			StatusPort: 2310,
			MaxPlayers: 10,
			UseA2S:     true,
		},
		{
			Name:       "satisfactory",
			Type:       "satisfactory",
			Host:       "54.37.128.230", // ssg.sh public IP
			Port:       7777,
			MaxPlayers: 4,
			UseA2S:     false,
		},
		// Servers below are not currently running - uncomment when active:
		// {
		// 	Name:       "factorio",
		// 	Type:       "factorio",
		// 	Host:       "54.37.128.230",
		// 	Port:       34197,
		// 	MaxPlayers: 32,
		// 	UseA2S:     false,
		// },
		// {
		// 	Name:       "bms",
		// 	Type:       "bms",
		// 	Host:       "alpha.ssg.sh", // Different server
		// 	Port:       2934,
		// 	MaxPlayers: 16,
		// 	UseA2S:     false,
		// },
		// {
		// 	Name:       "openrct2",
		// 	Type:       "openrct2",
		// 	Host:       "54.37.128.230",
		// 	Port:       11753,
		// 	MaxPlayers: 2,
		// 	UseA2S:     false,
		// },
	}
}

// NewCollector creates a new game server collector.
func NewCollector(servers []ServerConfig, metrics *Metrics, logger *observability.Logger) *Collector {
	if logger == nil {
		logger = observability.NewLogger("gameservers")
	}
	if servers == nil {
		servers = DefaultServers()
	}
	return &Collector{
		servers:  servers,
		metrics:  metrics,
		logger:   logger,
		interval: 30 * time.Second,
		statuses: make(map[string]*ServerStatus),
	}
}

// Start begins the background collection loop.
func (c *Collector) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)

	// Initial collection
	c.collect(ctx)

	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				c.logger.Info("game server collector stopped")
				return
			case <-ticker.C:
				c.collect(ctx)
			}
		}
	}()

	c.logger.Info(fmt.Sprintf("game server collector started (interval=%s, servers=%d)", c.interval, len(c.servers)))
}

// Stop halts the background collection.
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// GetStatus returns the latest status for a server.
func (c *Collector) GetStatus(name string) *ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.statuses[name]
}

// GetAllStatuses returns all current server statuses.
func (c *Collector) GetAllStatuses() map[string]*ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]*ServerStatus, len(c.statuses))
	for k, v := range c.statuses {
		result[k] = v
	}
	return result
}

func (c *Collector) collect(ctx context.Context) {
	var wg sync.WaitGroup

	for _, server := range c.servers {
		wg.Add(1)
		go func(srv ServerConfig) {
			defer wg.Done()
			c.queryServer(ctx, srv)
		}(server)
	}

	wg.Wait()
}

func (c *Collector) queryServer(ctx context.Context, srv ServerConfig) {
	start := time.Now()
	status := &ServerStatus{
		Name:       srv.Name,
		Type:       srv.Type,
		MaxPlayers: srv.MaxPlayers,
	}

	defer func() {
		status.QueryTime = time.Since(start)

		// Update metrics
		c.updateMetrics(srv, status)

		// Store status
		c.mu.Lock()
		c.statuses[srv.Name] = status
		c.mu.Unlock()
	}()

	// Try A2S query for supported servers
	if srv.UseA2S {
		queryPort := srv.QueryPort
		if queryPort == 0 {
			queryPort = srv.Port + 3
		}

		info, err := c.queryA2SInfo(ctx, srv.Host, queryPort)
		if err != nil {
			status.Error = err
			status.Online = false
			// Debug level - only log on verbose mode or when troubleshooting
			// c.logger.Info(fmt.Sprintf("%s: A2S query failed: %v", srv.Name, err))
		} else {
			status.Online = true
			status.Players = info.Players
			status.MaxPlayers = info.MaxPlayers
			return
		}
	}

	// Fall back to TCP port check
	port := srv.StatusPort
	if port == 0 {
		port = srv.Port
	}

	if c.checkTCPPort(ctx, srv.Host, port) {
		status.Online = true
		status.Players = 0 // Unknown without proper query
	} else {
		status.Online = false
		if status.Error == nil {
			status.Error = fmt.Errorf("port %d unreachable", port)
		}
	}
}

func (c *Collector) updateMetrics(srv ServerConfig, status *ServerStatus) {
	labels := []string{srv.Name, srv.Type}

	// Update up/down status
	if status.Online {
		c.metrics.ServerUp.WithLabelValues(labels...).Set(1)
	} else {
		c.metrics.ServerUp.WithLabelValues(labels...).Set(0)
	}

	// Update player counts
	c.metrics.Players.WithLabelValues(labels...).Set(float64(status.Players))
	c.metrics.MaxPlayers.WithLabelValues(labels...).Set(float64(status.MaxPlayers))

	// Update timestamps and durations
	c.metrics.LastCheck.WithLabelValues(labels...).Set(float64(time.Now().Unix()))
	c.metrics.QueryDuration.WithLabelValues(labels...).Set(status.QueryTime.Seconds())

	// Record errors
	if status.Error != nil {
		errType := "query_failed"
		if status.Error.Error() == "timeout" {
			errType = "timeout"
		}
		c.metrics.QueryErrors.WithLabelValues(srv.Name, srv.Type, errType).Inc()
	}
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
func (c *Collector) queryA2SInfo(ctx context.Context, host string, port int) (*A2SInfo, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Set deadline from context or default
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	conn.SetDeadline(deadline)

	// A2S_INFO request packet
	// Header: 0xFF 0xFF 0xFF 0xFF
	// Type: 0x54 (T)
	// Payload: "Source Engine Query\x00"
	a2sRequest := []byte{
		0xFF, 0xFF, 0xFF, 0xFF, 0x54,
		0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20,
		0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20,
		0x51, 0x75, 0x65, 0x72, 0x79, 0x00,
	}

	// Send initial request
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

	// Check response type
	responseType := buf[4]

	// Handle challenge response (0x41 = 'A')
	if responseType == 0x41 {
		if n < 9 {
			return nil, fmt.Errorf("challenge response too short")
		}
		// Append challenge token to request
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

	return c.parseA2SInfo(buf[:n])
}

func (c *Collector) parseA2SInfo(data []byte) (*A2SInfo, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("data too short for A2S_INFO")
	}

	// Skip header (4 bytes) + type (1 byte) + protocol (1 byte)
	idx := 6

	// Read null-terminated strings
	readString := func() (string, error) {
		start := idx
		for idx < len(data) && data[idx] != 0 {
			idx++
		}
		if idx >= len(data) {
			return "", fmt.Errorf("string not terminated")
		}
		s := string(data[start:idx])
		idx++ // Skip null terminator
		return s, nil
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

func (c *Collector) checkTCPPort(ctx context.Context, host string, port int) bool {
	addr := fmt.Sprintf("%s:%d", host, port)

	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
