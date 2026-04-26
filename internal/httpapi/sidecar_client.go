package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// containerState mirrors cmd/docker-inspect-sidecar's wire shape.
type containerState struct {
	Status string `json:"status"`
	Health string `json:"health,omitempty"`
}

var (
	errSidecarUnreachable = errors.New("sidecar unreachable")
	errSidecarFailed      = errors.New("sidecar returned 5xx")
	errUnknownContainer   = errors.New("sidecar returned 404 for container")
)

type sidecarClient struct {
	baseURL string
	http    *http.Client
}

func newSidecarClient(baseURL string) *sidecarClient {
	return &sidecarClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *sidecarClient) Inspect(ctx context.Context, name string) (containerState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/inspect/"+name, nil)
	if err != nil {
		return containerState{}, fmt.Errorf("build sidecar req: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return containerState{}, fmt.Errorf("%w: %v", errSidecarUnreachable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out containerState
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return containerState{}, fmt.Errorf("decode sidecar resp: %w", err)
		}
		return out, nil
	case http.StatusNotFound:
		return containerState{}, errUnknownContainer
	default:
		return containerState{}, fmt.Errorf("%w: status=%d", errSidecarFailed, resp.StatusCode)
	}
}
