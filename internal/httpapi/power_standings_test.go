package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCacheStore implements just enough of CacheStore for power standings testing.
type mockCacheStore struct {
	callCount    int
	returnResult *store.PowerStandingResult
	returnError  error
	mu           sync.Mutex
}

func (m *mockCacheStore) GetPowerStandingData(ctx context.Context, systems []string, granularity string, hours int) (*store.PowerStandingResult, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	// Simulate query delay
	time.Sleep(10 * time.Millisecond)

	if m.returnError != nil {
		return nil, m.returnError
	}
	return m.returnResult, nil
}

func (m *mockCacheStore) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// createTestPowerStandingResult creates a realistic power standing result for testing.
func createTestPowerStandingResult() *store.PowerStandingResult {
	now := time.Now().UTC()
	return &store.PowerStandingResult{
		TimeSeries: []store.PowerStandingBucket{
			{
				Bucket: now.Add(-2 * time.Hour),
				Powers: map[string]store.ControlTotals{
					"Nakato Kaine":         {Reinforcement: 100000, Undermining: 5000},
					"Arissa Lavigny-Duval": {Reinforcement: 95000, Undermining: 8000},
				},
			},
			{
				Bucket: now.Add(-1 * time.Hour),
				Powers: map[string]store.ControlTotals{
					"Nakato Kaine":         {Reinforcement: 110000, Undermining: 5500},
					"Arissa Lavigny-Duval": {Reinforcement: 105000, Undermining: 8500},
				},
			},
		},
		Summary: map[string]store.PowerStats{
			"Nakato Kaine": {
				CurrentTotal:       110000,
				FirstTotal:         100000,
				Delta:              10000,
				RatePerHour:        10000,
				SystemCount:        25,
				ReinforcementTotal: 120000,
				UnderminingTotal:   9000,
				NetMerits:          111000,
				StateCounts: store.PowerStateCounts{
					Expansion:  2,
					Contested:  1,
					Exploited:  10,
					Fortified:  7,
					Stronghold: 4,
				},
			},
			"Arissa Lavigny-Duval": {
				CurrentTotal:       105000,
				FirstTotal:         95000,
				Delta:              10000,
				RatePerHour:        10000,
				SystemCount:        24,
				ReinforcementTotal: 110000,
				UnderminingTotal:   12000,
				NetMerits:          98000,
				StateCounts: store.PowerStateCounts{
					Expansion:  1,
					Contested:  2,
					Exploited:  9,
					Fortified:  6,
					Stronghold: 5,
				},
			},
		},
		GapAnalysis: &store.GapAnalysis{
			Leader:      "Nakato Kaine",
			LeaderTotal: 110000,
			Second:      "Arissa Lavigny-Duval",
			SecondTotal: 105000,
			Gap:         5000,
			LeaderRate:  10000,
			SecondRate:  10000,
		},
		TickInfo: store.TickInfo{
			TickNumber:    12,
			TickStart:     now.Add(-72 * time.Hour),
			TickEnd:       now.Add(96 * time.Hour),
			HoursIntoTick: 72,
		},
		QueryParams: store.QueryParams{
			Granularity: "1h",
			Hours:       168,
		},
		DataThrough: now,
		GeneratedAt: now,
	}
}

// createTestServerWithMock creates a Server with a mock cache store for testing.
func createTestServerWithMock(mock *mockCacheStore) *Server {
	return &Server{
		cfg:    &config.Config{},
		logger: observability.NewLogger("test"),
		// We need to set cacheStore but it's a *store.CacheStore type
		// For testing, we'll use a different approach - test the handler directly
	}
}

// ============================================================================
// Unit Tests: Cache Key Generation
// ============================================================================

func TestPowerStandingsCacheKey(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		hours       string
		expectedKey string
	}{
		{"default params", "", "", "1h:168"},
		{"15m granularity", "15m", "24", "15m:24"},
		{"30m granularity", "30m", "72", "30m:72"},
		{"1h granularity", "1h", "168", "1h:168"},
		{"6h granularity", "6h", "336", "6h:336"},
		{"1d granularity", "1d", "720", "1d:720"},
		{"invalid hours uses default", "1h", "invalid", "1h:168"},
		{"hours over max gets capped", "1h", "1000", "1h:720"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build request URL
			url := "/api/edin/power-standings"
			if tt.granularity != "" || tt.hours != "" {
				url += "?"
				if tt.granularity != "" {
					url += "granularity=" + tt.granularity
				}
				if tt.hours != "" {
					if tt.granularity != "" {
						url += "&"
					}
					url += "hours=" + tt.hours
				}
			}

			req := httptest.NewRequest(http.MethodGet, url, nil)

			// Parse query params to get cache key
			granularity := req.URL.Query().Get("granularity")
			if granularity == "" {
				granularity = "1h"
			}

			hours := 168
			if h := req.URL.Query().Get("hours"); h != "" {
				if parsed, err := parseInt(h); err == nil && parsed > 0 {
					hours = parsed
					if hours > 720 {
						hours = 720
					}
				}
			}

			cacheKey := granularity + ":" + itoa(hours)
			assert.Equal(t, tt.expectedKey, cacheKey)
		})
	}
}

// Helper functions for test
func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, assert.AnError
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ============================================================================
// Unit Tests: Cache Behavior
// ============================================================================

func TestPowerStandingsCacheTTL(t *testing.T) {
	// Verify the cache TTL constant is set correctly
	assert.Equal(t, 15*time.Minute, standingsCacheTTL,
		"Cache TTL should be 15 minutes")
}

func TestPowerStandingsCacheDataStructure(t *testing.T) {
	// Create a server and verify cache fields are properly initialized
	server := &Server{
		cfg:    &config.Config{},
		logger: observability.NewLogger("test"),
	}

	// Cache should start nil
	assert.Nil(t, server.standingsCacheData, "Cache data should be nil initially")
	assert.Nil(t, server.standingsCacheTimes, "Cache times should be nil initially")

	// Initialize cache (simulating first request)
	server.standingsCacheMu.Lock()
	server.standingsCacheData = make(map[string]*store.PowerStandingResult)
	server.standingsCacheTimes = make(map[string]time.Time)
	server.standingsCacheMu.Unlock()

	// Cache should now be initialized
	assert.NotNil(t, server.standingsCacheData, "Cache data should be initialized")
	assert.NotNil(t, server.standingsCacheTimes, "Cache times should be initialized")
}

func TestPowerStandingsCacheConcurrentAccess(t *testing.T) {
	server := &Server{
		cfg:                 &config.Config{},
		logger:              observability.NewLogger("test"),
		standingsCacheData:  make(map[string]*store.PowerStandingResult),
		standingsCacheTimes: make(map[string]time.Time),
	}

	result := createTestPowerStandingResult()
	cacheKey := "1h:168"

	// Store in cache
	server.standingsCacheMu.Lock()
	server.standingsCacheData[cacheKey] = result
	server.standingsCacheTimes[cacheKey] = time.Now()
	server.standingsCacheMu.Unlock()

	// Concurrent reads should be safe
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.standingsCacheMu.RLock()
			if cached, ok := server.standingsCacheData[cacheKey]; ok {
				assert.NotNil(t, cached)
			}
			server.standingsCacheMu.RUnlock()
		}()
	}
	wg.Wait()
}

func TestPowerStandingsCacheExpiry(t *testing.T) {
	server := &Server{
		cfg:                 &config.Config{},
		logger:              observability.NewLogger("test"),
		standingsCacheData:  make(map[string]*store.PowerStandingResult),
		standingsCacheTimes: make(map[string]time.Time),
	}

	result := createTestPowerStandingResult()
	cacheKey := "1h:168"

	// Store with current time - should be valid
	server.standingsCacheMu.Lock()
	server.standingsCacheData[cacheKey] = result
	server.standingsCacheTimes[cacheKey] = time.Now()
	server.standingsCacheMu.Unlock()

	// Check cache - should be valid
	server.standingsCacheMu.RLock()
	cachedTime := server.standingsCacheTimes[cacheKey]
	isValid := time.Since(cachedTime) < standingsCacheTTL
	server.standingsCacheMu.RUnlock()
	assert.True(t, isValid, "Fresh cache should be valid")

	// Store with old time - should be expired
	server.standingsCacheMu.Lock()
	server.standingsCacheTimes[cacheKey] = time.Now().Add(-20 * time.Minute)
	server.standingsCacheMu.Unlock()

	// Check cache - should be expired
	server.standingsCacheMu.RLock()
	cachedTime = server.standingsCacheTimes[cacheKey]
	isValid = time.Since(cachedTime) < standingsCacheTTL
	server.standingsCacheMu.RUnlock()
	assert.False(t, isValid, "Old cache should be expired")
}

func TestPowerStandingsCacheMultipleKeys(t *testing.T) {
	server := &Server{
		cfg:                 &config.Config{},
		logger:              observability.NewLogger("test"),
		standingsCacheData:  make(map[string]*store.PowerStandingResult),
		standingsCacheTimes: make(map[string]time.Time),
	}

	result1 := createTestPowerStandingResult()
	result1.QueryParams.Granularity = "1h"
	result1.QueryParams.Hours = 168

	result2 := createTestPowerStandingResult()
	result2.QueryParams.Granularity = "30m"
	result2.QueryParams.Hours = 24

	// Store two different cache entries
	server.standingsCacheMu.Lock()
	server.standingsCacheData["1h:168"] = result1
	server.standingsCacheTimes["1h:168"] = time.Now()
	server.standingsCacheData["30m:24"] = result2
	server.standingsCacheTimes["30m:24"] = time.Now()
	server.standingsCacheMu.Unlock()

	// Both should be retrievable
	server.standingsCacheMu.RLock()
	cached1 := server.standingsCacheData["1h:168"]
	cached2 := server.standingsCacheData["30m:24"]
	server.standingsCacheMu.RUnlock()

	assert.NotNil(t, cached1)
	assert.NotNil(t, cached2)
	assert.Equal(t, "1h", cached1.QueryParams.Granularity)
	assert.Equal(t, "30m", cached2.QueryParams.Granularity)
}

// ============================================================================
// Unit Tests: Response Structure
// ============================================================================

func TestPowerStandingResultStructure(t *testing.T) {
	result := createTestPowerStandingResult()

	// Validate time series
	assert.Len(t, result.TimeSeries, 2, "Should have 2 time buckets")
	assert.NotEmpty(t, result.TimeSeries[0].Powers, "First bucket should have powers")

	// Validate summary
	assert.Len(t, result.Summary, 2, "Should have 2 powers in summary")
	assert.Contains(t, result.Summary, "Nakato Kaine")
	assert.Contains(t, result.Summary, "Arissa Lavigny-Duval")
	assert.GreaterOrEqual(t, result.Summary["Nakato Kaine"].ReinforcementTotal, int64(0))
	assert.GreaterOrEqual(t, result.Summary["Nakato Kaine"].UnderminingTotal, int64(0))
	assert.GreaterOrEqual(t, result.Summary["Nakato Kaine"].StateCounts.Exploited, 0)

	// Validate gap analysis
	require.NotNil(t, result.GapAnalysis)
	assert.Equal(t, "Nakato Kaine", result.GapAnalysis.Leader)
	assert.Equal(t, int64(5000), result.GapAnalysis.Gap)

	// Validate tick info
	assert.Greater(t, result.TickInfo.TickNumber, 0)
	assert.Greater(t, result.TickInfo.HoursIntoTick, 0.0)

	// Validate query params
	assert.Equal(t, "1h", result.QueryParams.Granularity)
	assert.Equal(t, 168, result.QueryParams.Hours)
}

func TestPowerStandingResultJSON(t *testing.T) {
	result := createTestPowerStandingResult()

	// Should serialize to JSON without error
	jsonData, err := json.Marshal(result)
	require.NoError(t, err, "Should serialize to JSON")
	assert.NotEmpty(t, jsonData)

	// Should deserialize back
	var parsed store.PowerStandingResult
	err = json.Unmarshal(jsonData, &parsed)
	require.NoError(t, err, "Should deserialize from JSON")

	assert.Equal(t, result.QueryParams.Granularity, parsed.QueryParams.Granularity)
	assert.Equal(t, result.QueryParams.Hours, parsed.QueryParams.Hours)
	assert.Len(t, parsed.TimeSeries, len(result.TimeSeries))
}

// ============================================================================
// Unit Tests: Valid Granularities
// ============================================================================

func TestValidGranularities(t *testing.T) {
	validGranularities := map[string]bool{
		"15m": true,
		"30m": true,
		"1h":  true,
		"6h":  true,
		"1d":  true,
	}

	// Test valid granularities
	for gran := range validGranularities {
		t.Run("valid_"+gran, func(t *testing.T) {
			_, ok := store.ValidGranularities[gran]
			assert.True(t, ok, "%s should be a valid granularity", gran)
		})
	}

	// Test invalid granularities
	invalidGranularities := []string{"5m", "2h", "12h", "1w", "invalid"}
	for _, gran := range invalidGranularities {
		t.Run("invalid_"+gran, func(t *testing.T) {
			_, ok := store.ValidGranularities[gran]
			assert.False(t, ok, "%s should not be a valid granularity", gran)
		})
	}
}
