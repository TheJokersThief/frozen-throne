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
	if resp.Request.URL.Path != "/repos/acme/web" && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("final = %s (%d)", resp.Request.URL, resp.StatusCode)
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
