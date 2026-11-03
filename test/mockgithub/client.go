package mockgithub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client drives the control API of a running mock.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a control client for the mock at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mockgithub: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Reset clears all state, recorded calls and faults.
func (c *Client) Reset(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/_mock/reset", nil, nil)
}

// SeedInstallation registers an App installation.
func (c *Client) SeedInstallation(ctx context.Context, in Installation) error {
	return c.call(ctx, http.MethodPost, "/_mock/installations", in, nil)
}

// SeedRepo registers a repository under an existing installation.
func (c *Client) SeedRepo(ctx context.Context, in Repo) error {
	return c.call(ctx, http.MethodPost, "/_mock/repos", in, nil)
}

// SeedPR registers (or replaces, by number) a pull request.
func (c *Client) SeedPR(ctx context.Context, in PR) error {
	return c.call(ctx, http.MethodPost, "/_mock/pulls", in, nil)
}
