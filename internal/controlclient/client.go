package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/ops"
)

// Client interacts with the control API REST surface.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New constructs a new client.
func New(baseURL, apiKey string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("baseURL cannot be empty")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api key cannot be empty")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	return &Client{
		baseURL: parsed.String(),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

// Status fetches the status for a given service.
func (c *Client) Status(ctx context.Context, service string) (*ops.ServiceStatus, error) {
	var result ops.ServiceStatus
	if err := c.getJSON(ctx, "/status/"+service, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Restart triggers a restart for the given service.
func (c *Client) Restart(ctx context.Context, service string) (*ops.RestartResult, error) {
	var result ops.RestartResult
	if err := c.postJSON(ctx, "/actions/"+service+"/restart", nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Logs retrieves the most recent logs for a service.
func (c *Client) Logs(ctx context.Context, service string, tail int) ([]ops.LogEntry, error) {
	endpoint := "/logs/" + service
	if tail > 0 {
		endpoint = fmt.Sprintf("%s?tail=%d", endpoint, tail)
	}
	var resp struct {
		Entries []ops.LogEntry `json:"entries"`
	}
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}
	return resp.Entries, nil
}

// RunPlaybook executes an Ansible playbook by name.
func (c *Client) RunPlaybook(ctx context.Context, name string, extraVars map[string]string) (*ops.AnsibleJob, error) {
	payload := map[string]any{
		"playbook":   name,
		"extra_vars": extraVars,
	}
	var result ops.AnsibleJob
	if err := c.postJSON(ctx, "/ansible/run", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateLLMSession invokes the LLM endpoint and returns the updated session.
func (c *Client) CreateLLMSession(ctx context.Context, sessionID, userID, message string) (*llm.Session, string, error) {
	payload := map[string]any{
		"user_id":    userID,
		"session_id": sessionID,
		"message":    message,
	}
	var result struct {
		Session llm.Session `json:"session"`
		Reply   string      `json:"reply"`
	}
	if err := c.postJSON(ctx, "/llm/session", payload, &result); err != nil {
		return nil, "", err
	}
	return &result.Session, result.Reply, nil
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

// PowerplayBatchResult holds the response from a batch powerplay query.
type PowerplayBatchResult struct {
	Systems []PowerplaySystemResult `json:"systems"`
	Count   int                     `json:"count"`
}

// DayzEconomy retrieves DayZ server economy statistics.
func (c *Client) DayzEconomy(ctx context.Context) (*ops.DayZEconomyStats, error) {
	var result ops.DayZEconomyStats
	if err := c.getJSON(ctx, "/dayz/economy", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DayzItemSearch searches for item spawn configurations in types.xml.
func (c *Client) DayzItemSearch(ctx context.Context, search string) (*ops.DayZItemSearchResult, error) {
	var result ops.DayZItemSearchResult
	if err := c.getJSON(ctx, "/dayz/items?search="+url.QueryEscape(search), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PowerplayBatch queries powerplay data for multiple systems in parallel.
func (c *Client) PowerplayBatch(ctx context.Context, systems []string, batchSize int) (*PowerplayBatchResult, error) {
	if batchSize <= 0 {
		batchSize = 10
	}
	payload := map[string]any{
		"systems":    systems,
		"batch_size": batchSize,
	}
	var result PowerplayBatchResult
	if err := c.postJSON(ctx, "/spansh/powerplay-batch", payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return c.do(req, target)
}

func (c *Client) postJSON(ctx context.Context, endpoint string, body any, target any) error {
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	return c.do(req, target)
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body any) (*http.Request, error) {
	fullURL, err := c.joinURL(endpoint)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) do(req *http.Request, target any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil || apiErr.Error == "" {
			return fmt.Errorf("control api error: %s", resp.Status)
		}
		return fmt.Errorf("control api error: %s", apiErr.Error)
	}

	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c *Client) joinURL(endpoint string) (string, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}

	endpoint = strings.TrimPrefix(endpoint, "/")
	pathOnly := endpoint
	rawQuery := ""
	if idx := strings.Index(endpoint, "?"); idx >= 0 {
		pathOnly = endpoint[:idx]
		rawQuery = endpoint[idx+1:]
	}
	base.Path = path.Join(base.Path, pathOnly)
	if rawQuery != "" {
		base.RawQuery = rawQuery
	}
	return base.String(), nil
}
