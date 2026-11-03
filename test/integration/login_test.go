//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The compose OAuth redirect points at a container-only host name. Login must
// follow it through the host-reachable address. This exercises the same
// CheckRedirect hook Login uses, against a redirecting server.
func TestRedirectHostRewrite(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("reached " + r.URL.Path + " host=" + r.Host))
	}))
	defer target.Close()
	tu, _ := url.Parse(target.URL)

	s := &Stack{HostRewrites: map[string]string{ComposeMockWeb: target.URL}}
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+ComposeMockWeb+"/login/oauth/authorize?x=1", http.StatusFound)
	}))
	defer hop.Close()

	c := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error { s.rewrite(req); return nil }}
	resp, err := c.Get(hop.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Request.URL.Host != tu.Host || resp.Request.URL.Path != "/login/oauth/authorize" || resp.Request.URL.RawQuery != "x=1" {
		t.Fatalf("final request %s (status %d)", resp.Request.URL, resp.StatusCode)
	}
}
