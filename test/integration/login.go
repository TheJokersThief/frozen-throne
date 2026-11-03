//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"
)

// Session is an HTTP client holding a logged-in app session cookie.
type Session struct {
	t     *testing.T
	base  string
	Login string
	c     *http.Client
}

// Login signs in as the seeded mock user login through the real OAuth flow:
// app /api/v1/auth/login -> mock /login/oauth/authorize -> app callback ->
// token exchange and user lookup against the mock.
//
// Redirect hosts: the app builds the authorize URL from FT_GITHUB_WEB_URL. In
// compose that is http://mockgithub:8080, a name only containers can resolve.
// The client therefore rewrites the host of every redirect found in
// Stack.HostRewrites to the host-reachable base URL before following it. The
// rewrite is general (any mapped host, any hop), so other stacks with internal
// names only need an entry in the map.
func (s *Stack) Login(t *testing.T, login string) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The mock signs in its "default user" when the authorize URL carries no
	// login hint, and the app does not add one.
	if err := s.Mock.SetDefaultUser(ctx, login); err != nil {
		t.Fatalf("set default user %q: %v", login, err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{
		Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			s.rewrite(req)
			return nil
		},
	}
	se := &Session{t: t, base: s.AppURL, Login: login, c: c}
	resp, err := c.Get(s.AppURL + "/api/v1/auth/login")
	if err != nil {
		t.Fatalf("login as %s: %v", login, err)
	}
	resp.Body.Close()
	var me struct {
		Login string `json:"login"`
	}
	if st := se.JSON(http.MethodGet, "/api/v1/auth/me", nil, &me); st != http.StatusOK || me.Login != login {
		t.Fatalf("login as %s: /auth/me = %d, login %q", login, st, me.Login)
	}
	return se
}

// rewrite points req at the host-reachable address when its host is one of the
// stack's internal names.
func (s *Stack) rewrite(req *http.Request) {
	to, ok := s.HostRewrites[req.URL.Host]
	if !ok {
		return
	}
	u, err := url.Parse(to)
	if err != nil {
		return
	}
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	req.Host = u.Host
}

// JSON performs an API call and decodes a JSON response into out (if non-nil
// and the body is not empty). Mutating verbs get the CSRF header and a JSON
// content type, as the real frontend sends. It returns the status code.
func (se *Session) JSON(method, path string, body, out any) int {
	se.t.Helper()
	status, data := se.Do(method, path, body)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			se.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return status
}

// Do performs an API call and returns status and raw body.
func (se *Session) Do(method, path string, body any) (int, []byte) {
	se.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			se.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, se.base+path, rd)
	if err != nil {
		se.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Requested-With", "frozen-throne")
	}
	resp, err := se.c.Do(req)
	if err != nil {
		se.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}
