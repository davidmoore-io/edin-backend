// Package authentik provides an API client for Authentik identity management.
package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrDuplicateUsername is returned by CreateUser when Authentik rejects the
// request because the username is already taken. Callers handling shadow-user
// idempotency should fall back to GetUserByUsername to recover the existing
// user's UUID.
var ErrDuplicateUsername = errors.New("authentik: username already exists")

// ErrUserNotFound is returned by GetUserByUsername (and the user-by-PK lookup
// helpers) when the requested user does not exist.
var ErrUserNotFound = errors.New("authentik: user not found")

// Client is an Authentik API client.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// User represents an Authentik user.
//
// UUID is the stable identifier ("uuid" in the API response) used by EDIN as
// the foreign key in commander.commanders.authentik_user_id. PK is the
// numeric ID and is fine for short-lived API path lookups, but must NOT be
// persisted as the link key — Authentik rebuilds PKs on data reload.
type User struct {
	PK       int       `json:"pk"`
	UUID     uuid.UUID `json:"uuid"`
	Username string    `json:"username"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	IsActive bool      `json:"is_active"`
	Type     string    `json:"type"`
	Groups   []Group   `json:"groups_obj,omitempty"`
	UID      string    `json:"uid"`
	Avatar   string    `json:"avatar,omitempty"`
	Path     string    `json:"path,omitempty"`
}

// CreateUserRequest is the payload accepted by CreateUser. Only fields that
// EDIN actually sets are exposed — the Authentik API accepts more, but we
// don't use them. Add fields here as they're needed.
//
// Path defaults to "users" on the Authentik side when empty; shadow users go
// under "users/edin-commanders".
//
// IsActive is a pointer so the zero value (false) is distinguishable from
// "not set" — Authentik defaults new users to active when the field is absent.
type CreateUserRequest struct {
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Path     string   `json:"path,omitempty"`
	IsActive *bool    `json:"is_active,omitempty"`
	Type     string   `json:"type,omitempty"`
	Groups   []string `json:"groups,omitempty"` // Group PKs (UUIDs)
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

// CreateUser creates a new Authentik user via POST /api/v3/core/users/.
//
// Returns ErrDuplicateUsername if Authentik responds 400 with a duplicate-
// username validation error (Authentik surfaces the conflict in the response
// body's "username" field). Other 4xx/5xx responses are returned verbatim
// in the error string for diagnostic purposes.
//
// On success, the returned User has the canonical PK and UUID assigned by
// Authentik; persist UUID, never PK.
func (c *Client) CreateUser(ctx context.Context, req CreateUserRequest) (*User, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal create user request: %w", err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/core/users/", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create user request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var user User
		if err := json.Unmarshal(respBody, &user); err != nil {
			return nil, fmt.Errorf("decode created user: %w", err)
		}
		return &user, nil
	case http.StatusBadRequest:
		// Authentik returns validation errors with shape:
		//   {"username": ["User with this username already exists."]}
		// Discriminate the duplicate-username case from other "username"-keyed
		// validation errors (e.g. "may not be blank", "too short") by requiring
		// the message to contain "already exists". A broader match — any 400
		// with a "username" key — would misclassify those generic validation
		// failures as duplicates, a footgun for future callers even though
		// today's shadow helper always sends a non-empty FID.
		var errs map[string][]string
		if jerr := json.Unmarshal(respBody, &errs); jerr == nil {
			if msgs, ok := errs["username"]; ok {
				for _, m := range msgs {
					if strings.Contains(m, "already exists") {
						return nil, ErrDuplicateUsername
					}
				}
			}
		}
		return nil, fmt.Errorf("create user failed: %s - %s", resp.Status, string(respBody))
	default:
		return nil, fmt.Errorf("create user failed: %s - %s", resp.Status, string(respBody))
	}
}

// GetUserByUsername returns the user whose username exactly matches the
// argument. Returns ErrUserNotFound if no such user exists.
//
// Authentik's list endpoint accepts ?username=... and returns a paginated
// result; we take the first match (Authentik enforces username uniqueness).
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	endpoint := "/core/users/?username=" + encodeQueryComponent(username)
	resp, err := c.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("get user by username request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get user by username failed: %s - %s", resp.Status, string(body))
	}

	var result PaginatedResponse[User]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode user by username: %w", err)
	}

	for _, u := range result.Results {
		if u.Username == username {
			user := u
			return &user, nil
		}
	}
	return nil, ErrUserNotFound
}

// encodeQueryComponent percent-encodes a URL query value. Kept package-local
// to avoid pulling net/url just for this one helper; Authentik usernames are
// ASCII-safe, but the generated shadow-user usernames could in principle
// contain commander-name characters that require encoding.
func encodeQueryComponent(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b = append(b, c)
		default:
			b = append(b, '%', upperhex[c>>4], upperhex[c&0xf])
		}
	}
	return string(b)
}
