// Package controlclient is the bot's typed HTTP client for the control-API.
// It exposes the kaine read endpoints the bot consumes (plasmium-buyers,
// ltd-buyers) plus /admin/diagnose, all auth-injected via TokenSource.
package controlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrSystemNotFound is returned by GetSystemWatchSnapshot when the requested
// slug isn't in the galaxy data. The bot's /watch handler treats this as a
// "system doesn't exist" branch and replies politely rather than failing
// the slash command.
var ErrSystemNotFound = errors.New("system not found")

// TokenSource abstracts authclient.Client so this package doesn't import it
// directly (avoids a cycle if authclient ever needs anything from here).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type Client struct {
	baseURL string
	tokens  TokenSource
	http    *http.Client
}

func New(baseURL string, tokens TokenSource) *Client {
	return &Client{
		baseURL: baseURL,
		tokens:  tokens,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) PlasmiumBuyers(ctx context.Context) (*PlasmiumBuyersResponse, error) {
	var out PlasmiumBuyersResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/kaine/mining/plasmium-buyers", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) LTDBuyers(ctx context.Context) (*LTDBuyersResponse, error) {
	var out LTDBuyersResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/kaine/mining/ltd-buyers", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSystemWatchSnapshot fetches the powerplay + faction state for one
// system identified by its slug. Returns ErrSystemNotFound on 404 — the
// caller's /watch handler depends on this sentinel to render a polite
// "system not found" ephemeral instead of a generic error.
func (c *Client) GetSystemWatchSnapshot(ctx context.Context, slug string) (*SystemWatchSnapshot, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("get auth token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/kaine/watcher/systems/"+slug, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSystemNotFound
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}

	var out SystemWatchSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *Client) Diagnose(ctx context.Context) (*DiagnoseReport, error) {
	body := map[string][]string{
		"checks": {"galaxy-reader", "edin-timescaledb", "eddn-timescaledb", "eddn-listener"},
	}
	var out DiagnoseReport
	if err := c.doJSON(ctx, http.MethodPost, "/admin/diagnose", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("get auth token: %w", err)
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(b))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
