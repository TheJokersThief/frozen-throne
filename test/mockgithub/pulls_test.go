package mockgithub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func seedRepo(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	if err := h.ctl.SeedInstallation(ctx, mockgithub.Installation{ID: 7, AccountLogin: "acme", AccountID: 1, AccountType: "Organization"}); err != nil {
		t.Fatal(err)
	}
	if err := h.ctl.SeedRepo(ctx, mockgithub.Repo{InstallationID: 7, ID: 11, Owner: "acme", Name: "web", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
}

func TestListOpenPullRequestsPaginates150(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	for i := 1; i <= 150; i++ {
		if err := h.ctl.SeedPR(ctx, mockgithub.PR{Owner: "acme", Repo: "web", Number: i, HeadSHA: fmt.Sprintf("sha%03d", i), Draft: i%10 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	// A closed PR must not appear.
	_ = h.ctl.SeedPR(ctx, mockgithub.PR{Owner: "acme", Repo: "web", Number: 151, HeadSHA: "closed", State: "closed"})

	prs, err := h.app(t).ListOpenPullRequests(ctx, 7, "acme", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 150 {
		t.Fatalf("want 150 PRs, got %d", len(prs))
	}
	seen := map[int]bool{}
	for _, p := range prs {
		if seen[p.Number] {
			t.Fatalf("duplicate PR %d", p.Number)
		}
		seen[p.Number] = true
		if p.HeadSHA != fmt.Sprintf("sha%03d", p.Number) || p.Draft != (p.Number%10 == 0) {
			t.Fatalf("bad PR %+v", p)
		}
	}
}

func TestPullsDefaultPageSizeAndPageParam(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	for i := 1; i <= 45; i++ {
		_ = h.ctl.SeedPR(ctx, mockgithub.PR{Owner: "acme", Repo: "web", Number: i, HeadSHA: "s"})
	}
	tok := installToken(t, h, 7)
	count := func(q string) int {
		code, body := do(t, "GET", h.srv.URL+"/repos/acme/web/pulls"+q, tok, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		var l []json.RawMessage
		_ = json.Unmarshal(body, &l)
		return len(l)
	}
	if n := count("?state=open"); n != 30 {
		t.Fatalf("default per_page: want 30, got %d", n)
	}
	if n := count("?state=open&page=2"); n != 15 {
		t.Fatalf("page 2: want 15, got %d", n)
	}
	if n := count("?state=closed"); n != 0 {
		t.Fatalf("closed: want 0, got %d", n)
	}
}

func TestPullsRejectForeignInstallationAndBadToken(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	_ = h.ctl.SeedInstallation(ctx, mockgithub.Installation{ID: 8, AccountLogin: "other", AccountType: "Organization"})
	if code, _ := do(t, "GET", h.srv.URL+"/repos/acme/web/pulls", installToken(t, h, 8), nil); code != 404 {
		t.Fatalf("foreign installation: want 404, got %d", code)
	}
	if code, _ := do(t, "GET", h.srv.URL+"/repos/acme/web/pulls", "ghs_nope", nil); code != 401 {
		t.Fatalf("bad token: want 401, got %d", code)
	}
}

func TestResetClearsState(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	if err := h.ctl.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := h.app(t).ListOpenPullRequests(ctx, 7, "acme", "web")
	if err == nil {
		t.Fatal("installation should be gone after reset")
	}
}

// installToken mints an installation token through the real HTTP flow.
func installToken(t *testing.T, h *harness, id int) string {
	t.Helper()
	jwt := signJWT(t, h.key, appID)
	code, body := do(t, "POST", fmt.Sprintf("%s/app/installations/%d/access_tokens", h.srv.URL, id), jwt, nil)
	if code != 201 {
		t.Fatalf("token: %d %s", code, body)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Token
}
