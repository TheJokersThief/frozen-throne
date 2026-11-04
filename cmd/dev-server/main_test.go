package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/store"
)

func startServer(t *testing.T, role string) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "dev.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := newServer(ctx, st, role, bytes.Repeat([]byte("s"), 32))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestLoginFlowAndSeededRepos(t *testing.T) {
	ts := startServer(t, "admin")
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar} // follows the relative authorize redirect and the callback redirect

	resp, err := c.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous me = %d, want 401", resp.StatusCode)
	}

	resp, err = c.Get(ts.URL + "/api/v1/auth/login?redirect=" + url.QueryEscape("/repos/acme/web"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// The dev server serves no SPA, so the landing page itself is a 404; what
	// matters is that the login chain ended on the requested path.
	t.Logf("final = %s (%d)", resp.Request.URL, resp.StatusCode)
	if resp.Request.URL.Path != "/repos/acme/web" {
		t.Fatalf("final = %s (%d), want path /repos/acme/web", resp.Request.URL, resp.StatusCode)
	}
	if len(jar.Cookies(mustURL(t, ts.URL))) == 0 {
		t.Fatal("no session cookie stored")
	}

	var me struct{ Login string }
	getJSON(t, c, ts.URL+"/api/v1/auth/me", &me)
	if me.Login != "octocat" {
		t.Fatalf("me = %+v", me)
	}

	var repos struct {
		Total int
		Items []struct{ Owner, Name string }
	}
	getJSON(t, c, ts.URL+"/api/v1/repos?limit=100", &repos)
	if repos.Total != 20 {
		t.Fatalf("total repos = %d, want 20", repos.Total)
	}
	found := false
	for _, r := range repos.Items {
		found = found || (r.Owner == "acme" && r.Name == "web")
	}
	if !found {
		t.Fatalf("acme/web not listed: %+v", repos.Items)
	}
}

// The web client (web/src/api/queryClient.ts) tells a CSRF rejection from a
// permission denial by matching /X-Requested-With/i against the 403 message.
// web/src/api/csrfMessage.test.ts pins the same literal on the other side.
func TestCSRFRejectionMessage(t *testing.T) {
	ts := startServer(t, "admin")
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(ts.URL + "/api/v1/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = c.Post(ts.URL+"/api/v1/auth/logout", "application/json", nil) // no X-Requested-With
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Code, Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || body.Message != "missing X-Requested-With header" {
		t.Fatalf("got %d %+v, want 403 with message %q", resp.StatusCode, body, "missing X-Requested-With header")
	}
}

func TestRoleNoneSeesNothing(t *testing.T) {
	ts := startServer(t, "none")
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(ts.URL + "/api/v1/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var repos struct{ Total int }
	getJSON(t, c, ts.URL+"/api/v1/repos", &repos)
	if repos.Total != 0 {
		t.Fatalf("total = %d, want 0", repos.Total)
	}
}

func TestCheckBindAddr(t *testing.T) {
	cases := []struct {
		addr        string
		allowRemote bool
		wantErr     bool
	}{
		{"127.0.0.1:8080", false, false},
		{"localhost:8080", false, false},
		{"[::1]:8080", false, false},
		{":8080", false, true},
		{"0.0.0.0:8080", false, true},
		{"[::]:8080", false, true},
		{"192.168.1.5:8080", false, true},
		{"example.com:8080", false, true},
		{":8080", true, false},
		{"0.0.0.0:8080", true, false},
		{"nonsense", false, true},
	}
	for _, c := range cases {
		err := checkBindAddr(c.addr, c.allowRemote)
		if (err != nil) != c.wantErr {
			t.Errorf("checkBindAddr(%q, %v) err = %v, wantErr %v", c.addr, c.allowRemote, err, c.wantErr)
		}
	}
}

func TestSessionSecret(t *testing.T) {
	if _, err := sessionSecret("short"); err == nil || !strings.Contains(err.Error(), "FT_DEV_SESSION_SECRET") {
		t.Fatalf("short secret err = %v, want error naming FT_DEV_SESSION_SECRET", err)
	}
	long := strings.Repeat("k", 32)
	if got, err := sessionSecret(long); err != nil || string(got) != long {
		t.Fatalf("32-byte secret = %q, %v", got, err)
	}
	if got, err := sessionSecret(""); err != nil || len(got) != 32 {
		t.Fatalf("empty secret should generate 32 random bytes, got %d, %v", len(got), err)
	}
}

func TestBadRole(t *testing.T) {
	if _, err := newDevPerms("root"); err == nil {
		t.Fatal("expected error")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func getJSON(t *testing.T, c *http.Client, u string, out any) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}
