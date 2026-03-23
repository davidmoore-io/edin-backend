package edsm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://www.edsm.net/api-system-v1/"
	defaultTimeout = 30 * time.Second
)

// Client wraps access to the EDSM API.
type Client struct {
	baseURL    string
	httpClient *http.Client
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

// SystemBodies queries /bodies for the target system.
func (c *Client) SystemBodies(ctx context.Context, systemName, systemID string) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, "bodies", params)
}

// SystemEstimatedValue queries /estimated-value for the target system.
func (c *Client) SystemEstimatedValue(ctx context.Context, systemName, systemID string) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, "estimated-value", params)
}

// SystemStations queries /stations for the target system.
func (c *Client) SystemStations(ctx context.Context, systemName, systemID string) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, "stations", params)
}

// StationMarket queries /stations/market for the target system/station/market.
func (c *Client) StationMarket(ctx context.Context, systemName, systemID, stationName, marketID string) (map[string]any, error) {
	params := buildStationParams(systemName, systemID, stationName, marketID)
	return c.get(ctx, "stations/market", params)
}

// StationShipyard queries /stations/shipyard for the target system/station/market.
func (c *Client) StationShipyard(ctx context.Context, systemName, systemID, stationName, marketID string) (map[string]any, error) {
	params := buildStationParams(systemName, systemID, stationName, marketID)
	return c.get(ctx, "stations/shipyard", params)
}

// StationOutfitting queries /stations/outfitting for the target system/station/market.
func (c *Client) StationOutfitting(ctx context.Context, systemName, systemID, stationName, marketID string) (map[string]any, error) {
	params := buildStationParams(systemName, systemID, stationName, marketID)
	return c.get(ctx, "stations/outfitting", params)
}

// SystemFactions queries /factions for the target system.
func (c *Client) SystemFactions(ctx context.Context, systemName, systemID string, showHistory bool) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	if showHistory {
		params.Set("showHistory", "1")
	}
	return c.get(ctx, "factions", params)
}

// SystemTraffic queries /traffic for the target system.
func (c *Client) SystemTraffic(ctx context.Context, systemName, systemID string) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, "traffic", params)
}

// SystemDeaths queries /deaths for the target system.
func (c *Client) SystemDeaths(ctx context.Context, systemName, systemID string) (map[string]any, error) {
	params, err := buildSystemParams(systemName, systemID)
	if err != nil {
		return nil, err
	}
	return c.get(ctx, "deaths", params)
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values) (map[string]any, error) {
	fullURL := c.baseURL + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ssg-control-agent/1.0 (+https://ssg.sh)")

	if params != nil && len(params) > 0 {
		req.URL.RawQuery = params.Encode()
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("edsm api error: %s", resp.Status)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func buildSystemParams(systemName, systemID string) (url.Values, error) {
	name := strings.TrimSpace(systemName)
	id := strings.TrimSpace(systemID)
	if name == "" && id == "" {
		return nil, errors.New("system name or id is required")
	}
	values := url.Values{}
	if name != "" {
		values.Set("systemName", name)
	}
	if id != "" {
		values.Set("systemId", id)
	}
	return values, nil
}

func buildStationParams(systemName, systemID, stationName, marketID string) url.Values {
	values := url.Values{}
	if trimmed := strings.TrimSpace(marketID); trimmed != "" {
		values.Set("marketId", trimmed)
	}
	if trimmed := strings.TrimSpace(systemName); trimmed != "" {
		values.Set("systemName", trimmed)
	}
	if trimmed := strings.TrimSpace(systemID); trimmed != "" {
		values.Set("systemId", trimmed)
	}
	if trimmed := strings.TrimSpace(stationName); trimmed != "" {
		values.Set("stationName", trimmed)
	}
	return values
}
