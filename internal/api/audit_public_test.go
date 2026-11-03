package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

func TestAuditVisibilityAndPaging(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol, dave := e.login("alice"), e.login("bob"), e.login("carol"), e.login("dave")
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"w"}`), 201)
	e.expect(e.do(alice, "POST", "/repos/acme/api/freeze", `{"reason":"a"}`), 201)
	e.expect(e.do(alice, "POST", "/orgs/acme/freeze", `{"reason":"o"}`), 201)
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)

	type page struct {
		Items      []auditDTO `json:"items"`
		NextBefore int64      `json:"next_before"`
	}
	var p page
	e.expect(e.do(alice, "GET", "/audit", nil), 200).decode(&p)
	if len(p.Items) != 4 || p.Items[0].Action != "thaw" || p.Items[0].Org != "acme" || p.Items[0].Repo != "acme/web" || p.NextBefore != 0 {
		t.Fatalf("alice audit = %+v", p)
	}
	// carol can only see web (and the org-wide entry as an org-visible member).
	e.expect(e.do(carol, "GET", "/audit", nil), 200).decode(&p)
	for _, it := range p.Items {
		if it.Repo == "acme/api" {
			t.Fatalf("carol sees api entry: %+v", it)
		}
	}
	if len(p.Items) != 3 {
		t.Fatalf("carol audit = %+v", p.Items)
	}
	e.expect(e.do(dave, "GET", "/audit", nil), 200).decode(&p)
	if len(p.Items) != 0 || p.Items == nil {
		t.Fatalf("dave audit = %+v", p)
	}
	e.expect(e.do(dave, "GET", "/audit?org=acme", nil), 404)
	e.expect(e.do(dave, "GET", "/audit?repo=acme/web", nil), 404)

	// Filters and cursor.
	e.expect(e.do(alice, "GET", "/audit?action=freeze&actor=alice", nil), 200).decode(&p)
	if len(p.Items) != 2 {
		t.Fatalf("filtered = %+v", p.Items)
	}
	e.expect(e.do(alice, "GET", "/audit?repo=acme/web", nil), 200).decode(&p)
	if len(p.Items) != 2 {
		t.Fatalf("repo filter = %+v", p.Items)
	}
	e.expect(e.do(alice, "GET", "/audit?limit=2", nil), 200).decode(&p)
	if len(p.Items) != 2 || p.NextBefore == 0 {
		t.Fatalf("page 1 = %+v", p)
	}
	firstLast := p.Items[1].ID
	next := p.NextBefore
	p = page{}
	e.expect(e.do(alice, "GET", "/audit?limit=2&before="+itoa(next), nil), 200).decode(&p)
	if len(p.Items) != 2 || p.Items[0].ID >= firstLast {
		t.Fatalf("page 2 = %+v", p)
	}
	e.expect(e.do(alice, "GET", "/audit?action=explode", nil), 400)
	e.expect(e.do(alice, "GET", "/audit?before=x", nil), 400)
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPublicStatusOptIn(t *testing.T) {
	e := newEnv(t)
	bob, carol := e.login("bob"), e.login("carol")

	// Off by default: identical 404 for unknown and non-opted-in repos.
	a := e.expect(e.do(nil, "GET", "/public/repos/acme/web/status", nil), 404)
	b := e.expect(e.do(nil, "GET", "/public/repos/acme/ghost/status", nil), 404)
	if a.Body.String() != b.Body.String() {
		t.Fatalf("404 bodies differ: %q vs %q", a.Body, b.Body)
	}
	bad1 := e.expect(e.do(nil, "GET", "/public/repos/acme/web/badge.svg", nil), 404)
	bad2 := e.expect(e.do(nil, "GET", "/public/repos/acme/ghost/badge.svg", nil), 404)
	if bad1.Body.String() != bad2.Body.String() || !strings.Contains(bad1.Body.String(), "unknown") ||
		bad1.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("badge 404 = %q", bad1.Body)
	}

	e.expect(e.do(carol, "PUT", "/repos/acme/web/public-status", `{"enabled":true}`), 403)
	e.expect(e.do(bob, "PUT", "/repos/acme/web/public-status", `{}`), 400)
	var out map[string]bool
	e.expect(e.do(bob, "PUT", "/repos/acme/web/public-status", `{"enabled":true}`), 200).decode(&out)
	if !out["enabled"] || !e.detail(bob, "/repos/acme/web").PublicStatus {
		t.Fatal("not enabled")
	}

	var ps publicStatusDTO
	e.expect(e.do(nil, "GET", "/public/repos/acme/web/status", nil), 200).decode(&ps)
	if ps.Frozen || ps.Repo != "acme/web" {
		t.Fatalf("status = %+v", ps)
	}
	badge := e.expect(e.do(nil, "GET", "/public/repos/acme/web/badge.svg", nil), 200)
	if !strings.Contains(badge.Body.String(), "open") || badge.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("badge = %q %v", badge.Body, badge.Header())
	}

	// Frozen manually: no reason or actor leaks, no "until".
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"secret plan"}`), 201)
	r := e.expect(e.do(nil, "GET", "/public/repos/acme/web/status", nil), 200)
	if strings.Contains(r.Body.String(), "secret") || strings.Contains(r.Body.String(), "bob") {
		t.Fatalf("leak: %s", r.Body)
	}
	r.decode(&ps)
	if !ps.Frozen || ps.Since == nil || ps.Until != nil {
		t.Fatalf("status = %+v", ps)
	}
	if !strings.Contains(e.do(nil, "GET", "/public/repos/acme/web/badge.svg", nil).Body.String(), "frozen") {
		t.Fatal("badge should say frozen")
	}
	// Window only: until is the window end.
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", e.window(-time.Hour, time.Hour)), 201)
	e.expect(e.do(nil, "GET", "/public/repos/acme/web/status", nil), 200).decode(&ps)
	if !ps.Frozen || ps.Until == nil || !ps.Until.Equal(e.now.Add(time.Hour)) {
		t.Fatalf("window status = %+v", ps)
	}
	// Removed repos disappear.
	if err := e.st.MarkReposRemoved(context.Background(), 1, []int64{10}, e.now); err != nil {
		t.Fatal(err)
	}
	e.expect(e.do(nil, "GET", "/public/repos/acme/web/status", nil), 404)
}

// --- permissions ---

func TestCachedPermissions(t *testing.T) {
	inner := &fakePerms{org: map[string]Role{"u": RoleOwner}, repo: map[string]map[int64]Role{"u": {1: RoleMaintainer}}}
	now := time.Unix(1000, 0)
	c := NewCachedPermissions(inner, time.Minute, func() time.Time { return now })
	a := Actor{User: model.User{ID: 5, Login: "u"}}
	repo := model.Repo{ID: 1}
	for i := 0; i < 3; i++ {
		if r, err := c.RepoRole(context.Background(), a, repo); err != nil || r != RoleMaintainer {
			t.Fatalf("role=%v err=%v", r, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1", inner.calls)
	}
	// Revocation is picked up after the TTL.
	inner.repo["u"][1] = RoleNone
	if r, _ := c.RepoRole(context.Background(), a, repo); r != RoleMaintainer {
		t.Fatal("expected cached value inside TTL")
	}
	now = now.Add(61 * time.Second)
	if r, _ := c.RepoRole(context.Background(), a, repo); r != RoleNone {
		t.Fatal("expected refreshed value after TTL")
	}
	// Errors are not cached.
	inner.err = ErrTokenInvalid
	other := model.Repo{ID: 2}
	if _, err := c.RepoRole(context.Background(), a, other); err == nil {
		t.Fatal("want error")
	}
	inner.err = nil
	before := inner.calls
	c.RepoRole(context.Background(), a, other)
	if inner.calls != before+1 {
		t.Fatal("error was cached")
	}
	// Org and repo keys do not collide.
	if r, _ := c.OrgRole(context.Background(), a, model.Installation{ID: 1}); r != RoleOwner {
		t.Fatalf("org role = %v", r)
	}
}

func TestGitHubPermissions(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/admin":
			w.Write([]byte(`{"permissions":{"admin":true,"push":true,"pull":true}}`))
		case "/repos/acme/maintain":
			w.Write([]byte(`{"permissions":{"maintain":true,"push":true,"pull":true}}`))
		case "/repos/acme/write":
			w.Write([]byte(`{"permissions":{"push":true,"pull":true}}`))
		case "/repos/acme/read":
			w.Write([]byte(`{"permissions":{"pull":true}}`))
		case "/user/memberships/orgs/acme":
			w.Write([]byte(`{"state":"active","role":"admin"}`))
		case "/user/memberships/orgs/beta":
			w.Write([]byte(`{"state":"active","role":"member"}`))
		case "/user/memberships/orgs/gamma":
			w.Write([]byte(`{"state":"pending","role":"admin"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer gh.Close()
	g := &GitHubPermissions{APIBase: gh.URL}
	ctx := context.Background()
	a := Actor{User: model.User{ID: 7, Login: "me"}, Token: "tok"}
	for name, want := range map[string]Role{"admin": RoleMaintainer, "maintain": RoleMaintainer, "write": RoleMember, "read": RoleMember, "private": RoleNone} {
		if got, err := g.RepoRole(ctx, a, model.Repo{Owner: "acme", Name: name}); err != nil || got != want {
			t.Errorf("repo %s = %v, %v; want %v", name, got, err, want)
		}
	}
	for login, want := range map[string]Role{"acme": RoleOwner, "beta": RoleMember, "gamma": RoleNone, "delta": RoleNone} {
		if got, err := g.OrgRole(ctx, a, model.Installation{AccountLogin: login, AccountType: "Organization"}); err != nil || got != want {
			t.Errorf("org %s = %v, %v; want %v", login, got, err, want)
		}
	}
	// Personal accounts: only the account holder is owner, without a call.
	if got, _ := g.OrgRole(ctx, a, model.Installation{AccountType: "User", AccountID: 7}); got != RoleOwner {
		t.Errorf("own account = %v", got)
	}
	if got, _ := g.OrgRole(ctx, a, model.Installation{AccountType: "User", AccountID: 8}); got != RoleNone {
		t.Errorf("other account = %v", got)
	}
	a.Token = "revoked"
	if _, err := g.RepoRole(ctx, a, model.Repo{Owner: "acme", Name: "admin"}); err != ErrTokenInvalid {
		t.Errorf("revoked token err = %v", err)
	}
}

func TestGitHubOAuthClient(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "sec" {
				w.Write([]byte(`{"error":"bad_verification_code"}`))
				return
			}
			w.Write([]byte(`{"access_token":"tok"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"id":9,"login":"zed","name":"Zed","avatar_url":"https://a/z.png"}`))
		}
	}))
	defer gh.Close()
	o := &GitHubOAuth{ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://ft/cb", WebBase: gh.URL, APIBase: gh.URL}
	u := o.AuthorizeURL("st")
	if !strings.HasPrefix(u, gh.URL+"/login/oauth/authorize?") || !strings.Contains(u, "client_id=cid") || !strings.Contains(u, "state=st") {
		t.Fatalf("authorize url = %s", u)
	}
	ctx := context.Background()
	if _, err := o.Exchange(ctx, "nope"); err != ErrBadCode {
		t.Fatalf("bad code err = %v", err)
	}
	tok, err := o.Exchange(ctx, "good")
	if err != nil || tok != "tok" {
		t.Fatalf("exchange = %q, %v", tok, err)
	}
	user, err := o.User(ctx, tok)
	if err != nil || user.ID != 9 || user.Login != "zed" {
		t.Fatalf("user = %+v, %v", user, err)
	}
}
