// Package authentik provides an API client for Authentik identity management.
package authentik

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an Authentik API client.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// User represents an Authentik user.
type User struct {
	PK       int      `json:"pk"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	IsActive bool     `json:"is_active"`
	Type     string   `json:"type"`
	Groups   []Group  `json:"groups_obj,omitempty"`
	UID      string   `json:"uid"`
	Avatar   string   `json:"avatar,omitempty"`
}

// Group represents an Authentik group.
type Group struct {
	PK          string `json:"pk"`
	Name        string `json:"name"`
	IsSuperuser bool   `json:"is_superuser"`
	NumPK       int    `json:"num_pk,omitempty"`
}

// OAuthConnection represents a user's OAuth source connection (e.g., Discord).
type OAuthConnection struct {
	PK         int    `json:"pk"`
	Identifier string `json:"identifier"` // Discord user ID
	SourceSlug string `json:"source_slug,omitempty"`
}

// UserWithConnection combines user data with their OAuth connection info.
type UserWithConnection struct {
	User
	DiscordID       string   `json:"discord_id,omitempty"`
	DiscordUsername string   `json:"discord_username,omitempty"`
	GroupNames      []string `json:"group_names"`
}

// PaginatedResponse wraps paginated API responses.
type PaginatedResponse[T any] struct {
	Pagination struct {
		Count    int  `json:"count"`
		Next     *int `json:"next"`
		Previous *int `json:"previous"`
	} `json:"pagination"`
	Results []T `json:"results"`
}

// NewClient creates a new Authentik API client.
func NewClient(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doRequest performs an authenticated API request.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	url := fmt.Sprintf("%s/api/v3%s", c.baseURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

// ListUsers returns all users (external type only, which are OAuth users).
func (c *Client) ListUsers(ctx context.Context) ([]UserWithConnection, error) {
	// Get external users (OAuth-linked users)
	resp, err := c.doRequest(ctx, "GET", "/core/users/?type=external&page_size=500", nil)
	if err != nil {
		return nil, fmt.Errorf("list users request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list users failed: %s - %s", resp.Status, string(body))
	}

	var result PaginatedResponse[User]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}

	// Get OAuth connections for all users to get Discord IDs
	connections, err := c.getDiscordConnections(ctx)
	if err != nil {
		// Log but don't fail - connections are supplementary
		connections = make(map[int]string)
	}

	users := make([]UserWithConnection, 0, len(result.Results))
	for _, u := range result.Results {
		uwc := UserWithConnection{
			User:       u,
			GroupNames: make([]string, 0, len(u.Groups)),
		}
		for _, g := range u.Groups {
			uwc.GroupNames = append(uwc.GroupNames, g.Name)
		}
		if discordID, ok := connections[u.PK]; ok {
			uwc.DiscordID = discordID
		}
		users = append(users, uwc)
	}

	return users, nil
}

// getDiscordConnections returns a map of user PK to Discord ID.
func (c *Client) getDiscordConnections(ctx context.Context) (map[int]string, error) {
	resp, err := c.doRequest(ctx, "GET", "/sources/user_connections/oauth/?source__slug=discord&page_size=500", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get connections failed: %s", resp.Status)
	}

	var result struct {
		Results []struct {
			User       int    `json:"user"`
			Identifier string `json:"identifier"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	connections := make(map[int]string)
	for _, conn := range result.Results {
		connections[conn.User] = conn.Identifier
	}
	return connections, nil
}

// GetUser returns a single user by PK.
func (c *Client) GetUser(ctx context.Context, pk int) (*UserWithConnection, error) {
	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/core/users/%d/", pk), nil)
	if err != nil {
		return nil, fmt.Errorf("get user request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get user failed: %s - %s", resp.Status, string(body))
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}

	// Get Discord connection
	connections, _ := c.getDiscordConnections(ctx)

	uwc := UserWithConnection{
		User:       user,
		GroupNames: make([]string, 0, len(user.Groups)),
	}
	for _, g := range user.Groups {
		uwc.GroupNames = append(uwc.GroupNames, g.Name)
	}
	if discordID, ok := connections[user.PK]; ok {
		uwc.DiscordID = discordID
	}

	return &uwc, nil
}

// ListGroups returns all groups that start with "kaine-".
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	resp, err := c.doRequest(ctx, "GET", "/core/groups/?page_size=100", nil)
	if err != nil {
		return nil, fmt.Errorf("list groups request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list groups failed: %s - %s", resp.Status, string(body))
	}

	var result PaginatedResponse[Group]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode groups: %w", err)
	}

	// Filter to only kaine- groups
	kaineGroups := make([]Group, 0)
	for _, g := range result.Results {
		if strings.HasPrefix(g.Name, "kaine-") {
			kaineGroups = append(kaineGroups, g)
		}
	}

	return kaineGroups, nil
}

// GetGroupByName returns a group by name.
func (c *Client) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/core/groups/?name=%s", name), nil)
	if err != nil {
		return nil, fmt.Errorf("get group request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get group failed: %s", resp.Status)
	}

	var result PaginatedResponse[Group]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode group: %w", err)
	}

	if len(result.Results) == 0 {
		return nil, nil
	}

	return &result.Results[0], nil
}

// AddUserToGroup adds a user to a group.
func (c *Client) AddUserToGroup(ctx context.Context, userPK int, groupName string) error {
	// First get the group to find its PK
	group, err := c.GetGroupByName(ctx, groupName)
	if err != nil {
		return fmt.Errorf("get group: %w", err)
	}
	if group == nil {
		return fmt.Errorf("group not found: %s", groupName)
	}

	// Get current user to get their current groups
	user, err := c.GetUser(ctx, userPK)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user not found: %d", userPK)
	}

	// Check if already in group
	for _, g := range user.GroupNames {
		if g == groupName {
			return nil // Already in group
		}
	}

	// Build list of group PKs including the new one
	groupPKs := make([]string, 0, len(user.Groups)+1)
	for _, g := range user.Groups {
		groupPKs = append(groupPKs, g.PK)
	}
	groupPKs = append(groupPKs, group.PK)

	// Update user's groups
	body := fmt.Sprintf(`{"groups":[%s]}`, strings.Join(wrapQuotes(groupPKs), ","))
	resp, err := c.doRequest(ctx, "PATCH", fmt.Sprintf("/core/users/%d/", userPK), strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("update user groups: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update user groups failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// RemoveUserFromGroup removes a user from a group.
func (c *Client) RemoveUserFromGroup(ctx context.Context, userPK int, groupName string) error {
	// Get current user
	user, err := c.GetUser(ctx, userPK)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user not found: %d", userPK)
	}

	// Build list of group PKs excluding the one to remove
	groupPKs := make([]string, 0, len(user.Groups))
	found := false
	for _, g := range user.Groups {
		if g.Name == groupName {
			found = true
			continue
		}
		groupPKs = append(groupPKs, g.PK)
	}

	if !found {
		return nil // Not in group anyway
	}

	// Update user's groups
	body := fmt.Sprintf(`{"groups":[%s]}`, strings.Join(wrapQuotes(groupPKs), ","))
	resp, err := c.doRequest(ctx, "PATCH", fmt.Sprintf("/core/users/%d/", userPK), strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("update user groups: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update user groups failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// wrapQuotes wraps each string in quotes for JSON array.
func wrapQuotes(strs []string) []string {
	result := make([]string, len(strs))
	for i, s := range strs {
		result[i] = `"` + s + `"`
	}
	return result
}
