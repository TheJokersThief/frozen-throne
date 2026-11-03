package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultBaseURL is the public GitHub REST API. Set Config.BaseURL for GitHub
// Enterprise Server ("https://ghe.example.com/api/v3") or tests.
const DefaultBaseURL = "https://api.github.com"

// CheckName is the name of the Check Run; make it required in branch
// protection.
const CheckName = "frozen-throne"

// CheckRun is the data posted for a Check Run. Status is always "completed".
type CheckRun struct {
	HeadSHA    string
	Conclusion string // "success" or "failure"
	Title      string
	Summary    string
}

// PullSummary is the part of a pull request the reconcile path needs.
type PullSummary struct {
	Number  int
	HeadSHA string
	Draft   bool
}

// API is the GitHub surface the Service needs. Client implements it.
type API interface {
	CreateCheckRun(ctx context.Context, installationID int64, owner, repo string, c CheckRun) (id int64, err error)
	UpdateCheckRun(ctx context.Context, installationID int64, owner, repo string, id int64, c CheckRun) error
	ListOpenPullRequests(ctx context.Context, installationID int64, owner, repo string) ([]PullSummary, error)
}

// HTTPError is a non-2xx GitHub response.
type HTTPError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("github: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Config configures a Client.
type Config struct {
	AppID      int64
	PrivateKey *rsa.PrivateKey
	BaseURL    string       // default DefaultBaseURL
	HTTPClient *http.Client // default: 30s timeout
	Now        func() time.Time
}

type cachedToken struct {
	token   string
	expires time.Time
}

// Client talks to GitHub as the App (JWT) and as an installation (token,
// cached and refreshed shortly before expiry).
type Client struct {
	appID  int64
	key    *rsa.PrivateKey
	base   string
	http   *http.Client
	now    func() time.Time
	mu     sync.Mutex
	tokens map[int64]cachedToken
}

var _ API = (*Client)(nil)

// NewClient validates cfg and returns a Client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.AppID == 0 || cfg.PrivateKey == nil {
		return nil, errors.New("github: AppID and PrivateKey are required")
	}
	c := &Client{
		appID:  cfg.AppID,
		key:    cfg.PrivateKey,
		base:   cfg.BaseURL,
		http:   cfg.HTTPClient,
		now:    cfg.Now,
		tokens: map[int64]cachedToken{},
	}
	if c.base == "" {
		c.base = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// tokenRefreshSkew refreshes tokens this long before they expire.
const tokenRefreshSkew = time.Minute

func (c *Client) installationToken(ctx context.Context, id int64, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[id]; ok && !force && c.now().Add(tokenRefreshSkew).Before(t.expires) {
		return t.token, nil
	}
	jwt, err := signAppJWT(c.appID, c.key, c.now())
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(id, 10) + "/access_tokens"
	if err := c.send(ctx, jwt, http.MethodPost, path, nil, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("github: empty installation token")
	}
	c.tokens[id] = cachedToken{token: out.Token, expires: out.ExpiresAt}
	return out.Token, nil
}

// asInstallation performs a request with the installation token, retrying
// once with a fresh token if GitHub rejects the cached one.
func (c *Client) asInstallation(ctx context.Context, id int64, method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := c.installationToken(ctx, id, attempt > 0)
		if err != nil {
			return err
		}
		err = c.send(ctx, tok, method, path, body, out)
		var he *HTTPError
		if attempt == 0 && errors.As(err, &he) && he.Status == http.StatusUnauthorized {
			continue
		}
		return err
	}
}

func (c *Client) send(ctx context.Context, bearer, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "frozen-throne")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := string(data)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return &HTTPError{Status: resp.StatusCode, Method: method, Path: path, Body: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

type checkRunBody struct {
	Name        string         `json:"name"`
	HeadSHA     string         `json:"head_sha,omitempty"`
	Status      string         `json:"status"`
	Conclusion  string         `json:"conclusion"`
	CompletedAt string         `json:"completed_at"`
	Output      map[string]any `json:"output"`
}

func (c *Client) checkBody(cr CheckRun, withSHA bool) checkRunBody {
	b := checkRunBody{
		Name:        CheckName,
		Status:      "completed",
		Conclusion:  cr.Conclusion,
		CompletedAt: c.now().UTC().Format(time.RFC3339),
		Output:      map[string]any{"title": cr.Title, "summary": cr.Summary},
	}
	if withSHA {
		b.HeadSHA = cr.HeadSHA
	}
	return b
}

// CreateCheckRun posts a completed Check Run on cr.HeadSHA.
func (c *Client) CreateCheckRun(ctx context.Context, installationID int64, owner, repo string, cr CheckRun) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + owner + "/" + repo + "/check-runs"
	if err := c.asInstallation(ctx, installationID, http.MethodPost, path, c.checkBody(cr, true), &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// UpdateCheckRun replaces the result of an existing Check Run.
func (c *Client) UpdateCheckRun(ctx context.Context, installationID int64, owner, repo string, id int64, cr CheckRun) error {
	path := "/repos/" + owner + "/" + repo + "/check-runs/" + strconv.FormatInt(id, 10)
	return c.asInstallation(ctx, installationID, http.MethodPatch, path, c.checkBody(cr, false), nil)
}

// ListOpenPullRequests returns every open PR of the repo, following pages.
func (c *Client) ListOpenPullRequests(ctx context.Context, installationID int64, owner, repo string) ([]PullSummary, error) {
	const perPage = 100
	var all []PullSummary
	for page := 1; page <= 100; page++ {
		var raw []struct {
			Number int  `json:"number"`
			Draft  bool `json:"draft"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=%d&page=%d", owner, repo, perPage, page)
		if err := c.asInstallation(ctx, installationID, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, p := range raw {
			all = append(all, PullSummary{Number: p.Number, HeadSHA: p.Head.SHA, Draft: p.Draft})
		}
		if len(raw) < perPage {
			break
		}
	}
	return all, nil
}
