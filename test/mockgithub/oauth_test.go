package mockgithub_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/api"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

const redirectURL = "http://app.test/api/v1/auth/callback"

func (h *harness) oauth() *api.GitHubOAuth {
	return &api.GitHubOAuth{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL, WebBase: h.srv.URL, APIBase: h.srv.URL}
}

// authorize follows the authorize URL without chasing the redirect.
func authorize(t *testing.T, authURL string) *http.Response {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func seedAlice(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.ctl.SeedUser(context.Background(), mockgithub.User{ID: 100, Login: "alice", Name: "Alice A"}); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthAuthorizeRedirectExchangeAndUser(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedAlice(t, h)
	if err := h.ctl.SetDefaultUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	o := h.oauth()

	resp := authorize(t, o.AuthorizeURL("state-xyz"))
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: want 302, got %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host+loc.Path != redirectURL {
		t.Fatalf("redirect target %s", loc)
	}
	if loc.Query().Get("state") != "state-xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect query %v", loc.Query())
	}

	tok, err := o.Exchange(ctx, loc.Query().Get("code"))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	u, err := o.User(ctx, tok)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if u.ID != 100 || u.Login != "alice" || u.Name != "Alice A" || u.AvatarURL == "" {
		t.Fatalf("user %+v", u)
	}

	// Codes are single use.
	if _, err := o.Exchange(ctx, loc.Query().Get("code")); !errors.Is(err, api.ErrBadCode) {
		t.Fatalf("reused code: want ErrBadCode, got %v", err)
	}
}

func TestOAuthLoginParamPicksUser(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedAlice(t, h)
	_, _ = h.ctl.SeedUser(ctx, mockgithub.User{ID: 101, Login: "bob"})
	_ = h.ctl.SetDefaultUser(ctx, "alice")
	o := h.oauth()

	resp := authorize(t, o.AuthorizeURL("s")+"&login=bob")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	tok, err := o.Exchange(ctx, loc.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := o.User(ctx, tok)
	if err != nil || u.Login != "bob" {
		t.Fatalf("want bob, got %+v (%v)", u, err)
	}
}

func TestOAuthBadCodeAndBadClient(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	o := h.oauth()
	if _, err := o.Exchange(ctx, "nope"); !errors.Is(err, api.ErrBadCode) {
		t.Fatalf("want ErrBadCode, got %v", err)
	}
	bad := h.oauth()
	bad.ClientSecret = "wrong"
	code, _ := h.ctl.SeedUser(ctx, mockgithub.User{ID: 1, Login: "u"})
	if _, err := bad.Exchange(ctx, code); err == nil || errors.Is(err, api.ErrBadCode) {
		t.Fatalf("bad secret: want incorrect_client_credentials error, got %v", err)
	}
	// The failed attempt must not burn the code.
	if _, err := o.Exchange(ctx, code); err != nil {
		t.Fatalf("code from SeedUser should still work: %v", err)
	}
}

func TestAuthorizeWithoutUserOrWrongClientFails(t *testing.T) {
	h := newHarness(t)
	o := h.oauth()
	if resp := authorize(t, o.AuthorizeURL("s")); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no user: want 422, got %d", resp.StatusCode)
	}
	seedAlice(t, h)
	_ = h.ctl.SetDefaultUser(context.Background(), "alice")
	o.ClientID = "other"
	if resp := authorize(t, o.AuthorizeURL("s")); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong client: want 404, got %d", resp.StatusCode)
	}
}

func TestUserEndpointRejectsUnknownToken(t *testing.T) {
	h := newHarness(t)
	_, err := h.oauth().User(context.Background(), "gho_unknown")
	if err == nil {
		t.Fatal("want error")
	}
}
