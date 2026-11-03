package mockgithub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// CheckRuns lists recorded check runs, filtered by any non-empty argument.
func (c *Client) CheckRuns(ctx context.Context, owner, repo, headSHA, name string) ([]CheckRun, error) {
	v := url.Values{"owner": {owner}, "repo": {repo}, "head_sha": {headSHA}, "name": {name}}
	for k := range v {
		if v.Get(k) == "" {
			v.Del(k)
		}
	}
	var out []CheckRun
	err := c.call(ctx, http.MethodGet, "/_mock/check-runs?"+v.Encode(), nil, &out)
	return out, err
}

// WaitForCheckRun polls until a check run on headSHA satisfies pred (nil
// accepts any) or ctx ends.
func (c *Client) WaitForCheckRun(ctx context.Context, owner, repo, headSHA string, pred func(CheckRun) bool) (CheckRun, error) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		runs, err := c.CheckRuns(ctx, owner, repo, headSHA, "")
		if err != nil {
			return CheckRun{}, err
		}
		for i := len(runs) - 1; i >= 0; i-- {
			if pred == nil || pred(runs[i]) {
				return runs[i], nil
			}
		}
		select {
		case <-ctx.Done():
			return CheckRun{}, fmt.Errorf("mockgithub: waiting for check run on %s/%s@%s: %w", owner, repo, headSHA, ctx.Err())
		case <-t.C:
		}
	}
}

// SeedUser creates or replaces a user and returns an OAuth code good for one
// token exchange (the user can also sign in through the authorize redirect).
func (c *Client) SeedUser(ctx context.Context, in User) (code string, err error) {
	var out struct {
		Code string `json:"code"`
	}
	err = c.call(ctx, http.MethodPost, "/_mock/users", in, &out)
	return out.Code, err
}

// SetDefaultUser chooses who the authorize redirect signs in when no login
// parameter is given.
func (c *Client) SetDefaultUser(ctx context.Context, login string) error {
	return c.call(ctx, http.MethodPut, "/_mock/default-user", map[string]string{"login": login}, nil)
}

// Calls returns the recorded REST/OAuth calls in arrival order.
func (c *Client) Calls(ctx context.Context) ([]Call, error) {
	var out []Call
	err := c.call(ctx, http.MethodGet, "/_mock/calls", nil, &out)
	return out, err
}

// ClearCalls forgets recorded calls but keeps all other state.
func (c *Client) ClearCalls(ctx context.Context) error {
	return c.call(ctx, http.MethodDelete, "/_mock/calls", nil, nil)
}

// AddFault makes the next f.Count requests matching f fail with f.Status.
func (c *Client) AddFault(ctx context.Context, f Fault) error {
	return c.call(ctx, http.MethodPost, "/_mock/faults", f, nil)
}
