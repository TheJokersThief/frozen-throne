package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// Role is a user's standing on a repo or org, ordered by privilege.
type Role int

const (
	RoleNone       Role = iota // cannot see it
	RoleMember                 // read-only
	RoleMaintainer             // GitHub repo admin/maintain (or org owner)
	RoleOwner                  // org owner
)

// ErrTokenInvalid means GitHub rejected the user's access token; the session
// is ended and the user must log in again.
var ErrTokenInvalid = errors.New("github rejected the access token")

// PermissionChecker answers live questions about what a user may do on
// GitHub. Implementations talk to GitHub with the user's own token.
type PermissionChecker interface {
	// RepoRole is the user's direct standing on the repo: RoleNone,
	// RoleMember (read) or RoleMaintainer (admin or maintain).
	RepoRole(ctx context.Context, a Actor, repo model.Repo) (Role, error)
	// OrgRole is the user's standing on the installation's account:
	// RoleNone, RoleMember (org member) or RoleOwner. For a personal account
	// only the account holder is RoleOwner.
	OrgRole(ctx context.Context, a Actor, inst model.Installation) (Role, error)
}

// CachedPermissions memoizes a PermissionChecker for a short TTL so every
// request does not hit GitHub, while revocations still take effect quickly.
// Errors are never cached.
type CachedPermissions struct {
	inner PermissionChecker
	ttl   time.Duration
	now   func() time.Time

	mu sync.Mutex
	m  map[cacheKey]cacheVal
}

type cacheKey struct {
	org  bool
	user int64
	id   int64
}

type cacheVal struct {
	role Role
	exp  time.Time
}

// NewCachedPermissions wraps inner.
func NewCachedPermissions(inner PermissionChecker, ttl time.Duration, now func() time.Time) *CachedPermissions {
	if now == nil {
		now = time.Now
	}
	return &CachedPermissions{inner: inner, ttl: ttl, now: now, m: map[cacheKey]cacheVal{}}
}

func (c *CachedPermissions) lookup(k cacheKey) (Role, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if !ok || !c.now().Before(v.exp) {
		return 0, false
	}
	return v.role, true
}

func (c *CachedPermissions) store(k cacheKey, r Role) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) > 10000 {
		for key, v := range c.m {
			if !now.Before(v.exp) {
				delete(c.m, key)
			}
		}
	}
	c.m[k] = cacheVal{role: r, exp: now.Add(c.ttl)}
}

// RepoRole implements PermissionChecker.
func (c *CachedPermissions) RepoRole(ctx context.Context, a Actor, repo model.Repo) (Role, error) {
	k := cacheKey{user: a.User.ID, id: repo.ID}
	if r, ok := c.lookup(k); ok {
		return r, nil
	}
	r, err := c.inner.RepoRole(ctx, a, repo)
	if err == nil {
		c.store(k, r)
	}
	return r, err
}

// OrgRole implements PermissionChecker.
func (c *CachedPermissions) OrgRole(ctx context.Context, a Actor, inst model.Installation) (Role, error) {
	k := cacheKey{org: true, user: a.User.ID, id: inst.ID}
	if r, ok := c.lookup(k); ok {
		return r, nil
	}
	r, err := c.inner.OrgRole(ctx, a, inst)
	if err == nil {
		c.store(k, r)
	}
	return r, err
}

// GitHubPermissions is the real PermissionChecker, using the user's OAuth
// token against the GitHub REST API.
type GitHubPermissions struct {
	APIBase string // default https://api.github.com
	HTTP    *http.Client
}

func (g *GitHubPermissions) get(ctx context.Context, token, path string, out any) (int, error) {
	base := orDefault(g.APIBase, "https://api.github.com")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return 0, err
	}
	setGitHubHeaders(req, token)
	cl := g.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return resp.StatusCode, ErrTokenInvalid
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// RepoRole implements PermissionChecker via GET /repos/{owner}/{repo}, whose
// `permissions` object reflects the calling user.
func (g *GitHubPermissions) RepoRole(ctx context.Context, a Actor, repo model.Repo) (Role, error) {
	var out struct {
		Permissions struct {
			Admin    bool `json:"admin"`
			Maintain bool `json:"maintain"`
			Push     bool `json:"push"`
			Triage   bool `json:"triage"`
			Pull     bool `json:"pull"`
		} `json:"permissions"`
	}
	status, err := g.get(ctx, a.Token, "/repos/"+url.PathEscape(repo.Owner)+"/"+url.PathEscape(repo.Name), &out)
	if err != nil {
		return RoleNone, err
	}
	switch status {
	case http.StatusOK:
		p := out.Permissions
		switch {
		case p.Admin || p.Maintain:
			return RoleMaintainer, nil
		case p.Push || p.Triage || p.Pull:
			return RoleMember, nil
		}
		return RoleNone, nil
	case http.StatusNotFound, http.StatusForbidden:
		return RoleNone, nil
	}
	return RoleNone, fmt.Errorf("github: GET repo %s: status %d", fullName(repo), status)
}

// OrgRole implements PermissionChecker via the authenticated user's org
// membership.
func (g *GitHubPermissions) OrgRole(ctx context.Context, a Actor, inst model.Installation) (Role, error) {
	if inst.AccountType != "Organization" {
		if inst.AccountID == a.User.ID {
			return RoleOwner, nil
		}
		return RoleNone, nil
	}
	var out struct {
		State string `json:"state"`
		Role  string `json:"role"`
	}
	status, err := g.get(ctx, a.Token, "/user/memberships/orgs/"+url.PathEscape(inst.AccountLogin), &out)
	if err != nil {
		return RoleNone, err
	}
	switch status {
	case http.StatusOK:
		if out.State != "active" {
			return RoleNone, nil
		}
		if out.Role == "admin" {
			return RoleOwner, nil
		}
		return RoleMember, nil
	case http.StatusNotFound, http.StatusForbidden:
		return RoleNone, nil
	}
	return RoleNone, fmt.Errorf("github: GET org membership %s: status %d", inst.AccountLogin, status)
}
