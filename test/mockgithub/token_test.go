package mockgithub_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func TestInstallationTokenFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.ctl.SeedInstallation(ctx, mockgithub.Installation{ID: 7, AccountLogin: "acme", AccountID: 1, AccountType: "Organization"}); err != nil {
		t.Fatal(err)
	}
	if err := h.ctl.SeedRepo(ctx, mockgithub.Repo{InstallationID: 7, ID: 11, Owner: "acme", Name: "web", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}

	// The real client mints a JWT, exchanges it for a token and uses it.
	prs, err := h.app(t).ListOpenPullRequests(ctx, 7, "acme", "web")
	if err != nil {
		t.Fatalf("real client against mock: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("want no PRs, got %d", len(prs))
	}
}

func TestBadJWTSignatureIs401(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.ctl.SeedInstallation(ctx, mockgithub.Installation{ID: 7, AccountLogin: "acme", AccountType: "Organization"})

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := github.NewClient(github.Config{AppID: appID, PrivateKey: other, BaseURL: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.ListOpenPullRequests(ctx, 7, "acme", "web")
	var he *github.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("want 401 HTTPError, got %v", err)
	}
}

func TestWrongAppIDIs401(t *testing.T) {
	h := newHarness(t)
	_ = h.ctl.SeedInstallation(context.Background(), mockgithub.Installation{ID: 7, AccountLogin: "acme", AccountType: "Organization"})
	c, _ := github.NewClient(github.Config{AppID: appID + 1, PrivateKey: h.key, BaseURL: h.srv.URL})
	_, err := c.ListOpenPullRequests(context.Background(), 7, "acme", "web")
	var he *github.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", err)
	}
}

func TestMissingBearerIs401AndUnknownInstallationIs404(t *testing.T) {
	h := newHarness(t)
	if code, _ := do(t, "POST", h.srv.URL+"/app/installations/7/access_tokens", "", nil); code != 401 {
		t.Fatalf("no auth: want 401, got %d", code)
	}
	_, err := h.app(t).ListOpenPullRequests(context.Background(), 999, "a", "b")
	var he *github.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("unknown installation: want 404, got %v", err)
	}
}
