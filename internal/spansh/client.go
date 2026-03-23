package spansh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL       = "https://spansh.co.uk/api/"
	defaultRateLimitWait = time.Second
	defaultTimeout       = 45 * time.Second
	pollAttempts         = 6
	fleetPollAttempts    = 20 // ~3 minutes of polling with exponential backoff
)

// Client provides convenience helpers for querying the Spansh API.
type Client struct {
	baseURL    string
	httpClient *http.Client

	mu          sync.Mutex
	lastRequest time.Time
}

// NewClient constructs a client with sane defaults.
func NewClient() *Client {
	return &Client{
		baseURL: strings.TrimRight(defaultBaseURL, "/") + "/",
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// Operation enumerates supported high-level operations.
type Operation string

const (
	OpStationsSelling     Operation = "stations_selling"
	OpPowerplaySystems    Operation = "powerplay_systems"
	OpFactionStateSystems Operation = "faction_state_systems"
	OpGenericSystems      Operation = "generic_systems"
	OpFleetCarrierRoute   Operation = "fleet_carrier_route"
	OpSystemLookup        Operation = "system_lookup"
	OpHealth              Operation = "health"
)

// Execute performs the requested Spansh operation with arbitrary parameters.
// Parameters should be simple JSON-compatible values (strings, floats, ints, bools, maps).
func (c *Client) Execute(ctx context.Context, op Operation, params map[string]any) (map[string]any, error) {
	switch op {
	case OpStationsSelling:
		return c.stationsSelling(ctx, params)
	case OpPowerplaySystems:
		return c.powerplaySystems(ctx, params)
	case OpFactionStateSystems:
		return c.factionStateSystems(ctx, params)
	case OpGenericSystems:
		return c.genericSystems(ctx, params)
	case OpFleetCarrierRoute:
		return c.fleetCarrierRoute(ctx, params)
	case OpSystemLookup:
		return c.systemLookup(ctx, params)
	case OpHealth:
		return c.health(ctx)
	default:
		return nil, fmt.Errorf("unsupported spansh operation: %s", op)
	}
}

func (c *Client) stationsSelling(ctx context.Context, params map[string]any) (map[string]any, error) {
	commodity := strings.TrimSpace(getString(params, "commodity"))
	reference := strings.TrimSpace(getString(params, "reference_system"))
	if commodity == "" {
		return nil, errors.New("commodity is required")
	}
	if reference == "" {
		return nil, errors.New("reference_system is required")
	}

	limit := getInt(params, "limit", 3)
	if limit <= 0 {
		limit = 3
	}

	search := map[string]any{
		"filters": map[string]any{
			"has_market": map[string]any{"value": true},
			"has_large_pad": map[string]any{
				"value": getBool(params, "has_large_pad", true),
			},
			"market": []map[string]any{
				{
					"name": commodity,
					"supply": map[string]any{
						"value":      []string{"1", "999999999"},
						"comparison": "<=>",
					},
				},
			},
			"type": map[string]any{
				"value": []string{
					"Asteroid base",
					"Coriolis Starport",
					"Dockable Planet Station",
					"Mega ship",
					"Ocellus Starport",
					"Orbis Starport",
					"Outpost",
					"Planetary Construction Depot",
					"Planetary Outpost",
					"Planetary Port",
					"Settlement",
					"Space Construction Depot",
					"Surface Settlement",
				},
			},
		},
		"sort": []map[string]any{
			{"distance": map[string]any{"direction": "asc"}},
		},
		"size":             limit,
		"page":             0,
		"reference_system": reference,
	}

	if maxDist, ok := getFloatOpt(params, "max_distance"); ok {
		minDist, _ := getFloatOpt(params, "min_distance")
		search["filters"].(map[string]any)["distance"] = map[string]any{
			"min": fmt.Sprintf("%d", int(minDist)),
			"max": fmt.Sprintf("%d", int(maxDist)),
		}
	} else if jumpRange, ok := getFloatOpt(params, "ship_laden_range"); ok && jumpRange > 0 {
		search["filters"].(map[string]any)["distance"] = map[string]any{
			"min": "0",
			"max": fmt.Sprintf("%d", int(jumpRange*2)),
		}
	}

	if econ := strings.TrimSpace(getString(params, "primary_economy")); econ != "" {
		search["filters"].(map[string]any)["primary_economy"] = map[string]any{
			"value": []string{econ},
		}
	}

	return c.submitSearch(ctx, "stations/search/save", search)
}

func (c *Client) powerplaySystems(ctx context.Context, params map[string]any) (map[string]any, error) {
	reference := strings.TrimSpace(getString(params, "reference_system"))
	if reference == "" {
		return nil, errors.New("reference_system is required")
	}
	maxDist := getFloat(params, "max_distance", 50)
	limit := getInt(params, "limit", 3)

	filters := map[string]any{
		"distance": map[string]any{
			"min": "0",
			"max": fmt.Sprintf("%d", int(maxDist)),
		},
	}
	if power := strings.TrimSpace(getString(params, "power_filter")); power != "" {
		if strings.EqualFold(power, "none") {
			filters["power"] = map[string]any{"value": nil}
		} else {
			filters["power"] = map[string]any{"value": []string{power}}
		}
	}
	if state := strings.TrimSpace(getString(params, "power_state")); state != "" {
		filters["power_state"] = map[string]any{"value": []string{state}}
	}

	search := map[string]any{
		"filters": filters,
		"sort": []map[string]any{
			{"distance": map[string]any{"direction": "asc"}},
		},
		"size":             limit,
		"page":             0,
		"reference_system": reference,
	}

	return c.submitSearch(ctx, "systems/search/save", search)
}

func (c *Client) factionStateSystems(ctx context.Context, params map[string]any) (map[string]any, error) {
	reference := strings.TrimSpace(getString(params, "reference_system"))
	state := strings.TrimSpace(getString(params, "controlling_faction_state"))
	if reference == "" {
		return nil, errors.New("reference_system is required")
	}
	if state == "" {
		return nil, errors.New("controlling_faction_state is required")
	}
	maxDist := getFloat(params, "max_distance", 50)
	limit := getInt(params, "limit", 3)

	filters := map[string]any{
		"distance": map[string]any{
			"min": "0",
			"max": fmt.Sprintf("%d", int(maxDist)),
		},
		"controlling_minor_faction_state": map[string]any{
			"value": []string{state},
		},
	}

	if hasLargePad, ok := getBoolOpt(params, "has_large_pad"); ok {
		filters["has_large_pad"] = map[string]any{"value": hasLargePad}
	}
	if hasMarket, ok := getBoolOpt(params, "has_market"); ok {
		filters["has_market"] = map[string]any{"value": hasMarket}
	}
	if power := strings.TrimSpace(getString(params, "power_filter")); power != "" {
		filters["power"] = map[string]any{"value": []string{power}}
	}
	if econ := strings.TrimSpace(getString(params, "economy_filter")); econ != "" {
		filters["primary_economy"] = map[string]any{"value": []string{econ}}
	}
	if security := strings.TrimSpace(getString(params, "security_filter")); security != "" {
		filters["security"] = map[string]any{"value": []string{security}}
	}
	if minPopulation, ok := getFloatOpt(params, "min_population"); ok {
		filters["population"] = map[string]any{
			"value":      []string{fmt.Sprintf("%d", int(minPopulation)), "999999999999"},
			"comparison": "<=>",
		}
	}

	search := map[string]any{
		"filters": filters,
		"sort": []map[string]any{
			{"distance": map[string]any{"direction": "asc"}},
		},
		"size":             limit,
		"page":             0,
		"reference_system": reference,
	}

	return c.submitSearch(ctx, "systems/search/save", search)
}

func (c *Client) genericSystems(ctx context.Context, params map[string]any) (map[string]any, error) {
	reference := strings.TrimSpace(getString(params, "reference_system"))
	if reference == "" {
		return nil, errors.New("reference_system is required")
	}
	maxDist := getFloat(params, "max_distance", 50)
	minDist := getFloat(params, "min_distance", 0)
	limit := getInt(params, "limit", 10)

	filters := map[string]any{
		"distance": map[string]any{
			"min": fmt.Sprintf("%d", int(minDist)),
			"max": fmt.Sprintf("%d", int(maxDist)),
		},
	}

	for _, field := range []string{"power_filter", "power_state", "economy_filter", "secondary_economy_filter", "security_filter", "government_filter"} {
		if value := strings.TrimSpace(getString(params, field)); value != "" {
			key := strings.TrimSuffix(field, "_filter")
			filters[key] = map[string]any{"value": []string{value}}
		}
	}
	if minPopulation, ok := getFloatOpt(params, "min_population"); ok {
		filters["population"] = map[string]any{
			"value":      []string{fmt.Sprintf("%d", int(minPopulation)), "999999999999"},
			"comparison": "<=>",
		}
	}
	if hasLargePad, ok := getBoolOpt(params, "has_large_pad"); ok {
		filters["has_large_pad"] = map[string]any{"value": hasLargePad}
	}
	if hasMarket, ok := getBoolOpt(params, "has_market"); ok {
		filters["has_market"] = map[string]any{"value": hasMarket}
	}

	search := map[string]any{
		"filters": filters,
		"sort": []map[string]any{
			{"distance": map[string]any{"direction": "asc"}},
		},
		"size":             limit,
		"page":             0,
		"reference_system": reference,
	}

	return c.submitSearch(ctx, "systems/search/save", search)
}

func (c *Client) systemLookup(ctx context.Context, params map[string]any) (map[string]any, error) {
	name := strings.TrimSpace(getString(params, "system_name"))
	if name == "" {
		return nil, errors.New("system_name is required")
	}

	search := map[string]any{
		"filters": map[string]any{
			"name": map[string]any{"value": name},
		},
		"sort": []map[string]any{
			{"distance": map[string]any{"direction": "asc"}},
		},
		"size":             1,
		"page":             0,
		"reference_system": name,
	}

	result, err := c.submitSearch(ctx, "systems/search/save", search)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return result, nil
	}

	// For lookup we only care about the first result.
	if entries, ok := result["results"].([]any); ok && len(entries) > 0 {
		result["result"] = entries[0]
	}
	return result, nil
}

func (c *Client) fleetCarrierRoute(ctx context.Context, params map[string]any) (map[string]any, error) {
	source := strings.TrimSpace(getString(params, "source_system"))
	destination := strings.TrimSpace(getString(params, "destination_system"))
	if source == "" || destination == "" {
		return nil, errors.New("source_system and destination_system are required")
	}

	capacity := getInt(params, "capacity", 25000)
	if capacity <= 0 {
		capacity = 25000
	}
	capacityUsed := getInt(params, "capacity_used", 0)
	calcFuel := getBool(params, "calculate_starting_fuel", true)

	form := url.Values{}
	form.Set("source", source)
	form.Set("destinations", destination)
	form.Set("capacity", fmt.Sprintf("%d", capacity))
	form.Set("capacity_used", fmt.Sprintf("%d", capacityUsed))
	if calcFuel {
		form.Set("calculate_starting_fuel", "1")
	} else {
		form.Set("calculate_starting_fuel", "0")
	}

	payload, err := c.postForm(ctx, "fleetcarrier/route", form)
	if err != nil {
		return nil, err
	}

	jobID, ok := payload["job"].(string)
	if !ok || jobID == "" {
		return nil, errors.New("fleet carrier route did not return a job identifier")
	}

	return c.pollFleetJob(ctx, jobID)
}

// FleetCarrierResult fetches the completed results for an existing fleet carrier routing job.
func (c *Client) FleetCarrierResult(ctx context.Context, jobID string) (map[string]any, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("job_id is required")
	}
	return c.pollFleetJob(ctx, jobID)
}

func (c *Client) health(ctx context.Context) (map[string]any, error) {
	resp, err := c.getJSON(ctx, "")
	if err != nil {
		return nil, err
	}
	resp["status"] = "healthy"
	resp["base_url"] = c.baseURL
	return resp, nil
}

func (c *Client) submitSearch(ctx context.Context, endpoint string, payload map[string]any) (map[string]any, error) {
	response, err := c.postJSON(ctx, endpoint, payload)
	if err != nil {
		return nil, err
	}
	ref, _ := response["search_reference"].(string)
	if ref == "" {
		return nil, errors.New("search_reference missing from response")
	}
	return c.pollSearch(ctx, ref)
}

func (c *Client) pollSearch(ctx context.Context, reference string) (map[string]any, error) {
	endpoint := fmt.Sprintf("systems/search/recall/%s", reference)
	if strings.HasPrefix(reference, "station") || strings.HasPrefix(reference, "commodity") {
		endpoint = fmt.Sprintf("stations/search/recall/%s", reference)
	}

	var lastErr error
	delay := 500 * time.Millisecond
	for attempt := 0; attempt < pollAttempts; attempt++ {
		result, err := c.getJSON(ctx, endpoint)
		if err == nil {
			if len(result) == 0 {
				lastErr = errors.New("empty response from spansh")
			} else if _, ok := result["results"]; ok {
				return result, nil
			} else if state, ok := result["state"].(string); ok && strings.EqualFold(state, "processing") {
				lastErr = errors.New("search still processing")
			} else {
				return result, nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			delay *= 2
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timed out waiting for spansh search result")
	}
	return nil, lastErr
}

func (c *Client) pollFleetJob(ctx context.Context, jobID string) (map[string]any, error) {
	url := fmt.Sprintf("https://spansh.co.uk/api/results/%s", jobID)
	var lastErr error
	delay := 2 * time.Second

	for attempt := 0; attempt < fleetPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		result, err := c.getAbsoluteJSON(ctx, url)
		if err != nil {
			lastErr = err
		} else if status, ok := result["status"].(string); ok {
			switch strings.ToLower(status) {
			case "ok":
				return result, nil
			case "failed", "error":
				return nil, fmt.Errorf("spansh job failed: %v", result["error"])
			case "in_progress", "processing":
				lastErr = errors.New("job still processing")
			default:
				return result, nil
			}
		} else {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			delay = time.Duration(float64(delay) * 1.5)
			if delay > 15*time.Second {
				delay = 15 * time.Second
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timed out waiting for fleet carrier job")
	}
	return nil, lastErr
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (map[string]any, error) {
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	return c.do(req)
}

func (c *Client) getJSON(ctx context.Context, endpoint string) (map[string]any, error) {
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) getAbsoluteJSON(ctx context.Context, fullURL string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req)
	return c.do(req)
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	full := c.baseURL + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req)
	return req, nil
}

func (c *Client) applyCommonHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ssg-control-agent/1.0 (+https://ssg.sh)")
	req.Header.Set("Origin", "https://www.spansh.co.uk")
	req.Header.Set("Referer", "https://www.spansh.co.uk/")
}

func (c *Client) do(req *http.Request) (map[string]any, error) {
	c.waitForRateLimit()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("spansh api error: %s", resp.Status)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) waitForRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if since := time.Since(c.lastRequest); since < defaultRateLimitWait {
		time.Sleep(defaultRateLimitWait - since)
	}
	c.lastRequest = time.Now()
}

// Utility helpers for parsing.

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		}
	}
	return ""
}

func getInt(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case float64:
			return int(typed)
		case float32:
			return int(typed)
		case int:
			return typed
		case int64:
			return int(typed)
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := fmt.Sscanf(typed, "%d", &fallback); err == nil && n == 1 {
				return fallback
			}
		}
	}
	return fallback
}

func getFloat(m map[string]any, key string, fallback float64) float64 {
	value, ok := getFloatOpt(m, key)
	if !ok {
		return fallback
	}
	return value
}

func getFloatOpt(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case float64:
			return typed, true
		case float32:
			return float64(typed), true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case json.Number:
			if f, err := typed.Float64(); err == nil {
				return f, true
			}
		case string:
			if f, err := strconv.ParseFloat(typed, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func getBool(m map[string]any, key string, fallback bool) bool {
	value, ok := getBoolOpt(m, key)
	if !ok {
		return fallback
	}
	return value
}

func getBoolOpt(m map[string]any, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case bool:
			return typed, true
		case string:
			if typed == "" {
				return false, false
			}
			if typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes") {
				return true, true
			}
			if typed == "0" || strings.EqualFold(typed, "false") || strings.EqualFold(typed, "no") {
				return false, true
			}
		}
	}
	return false, false
}
