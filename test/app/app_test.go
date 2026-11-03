package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/app"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

const (
	appID         = 424242
	webhookSecret = "whsec_test"
	clientID      = "Iv1.testclient"
	clientSecret  = "testclientsecret"
)

type stack struct {
	app  *httptest.Server
	mock *httptest.Server
	ctl  *mockgithub.Client
}

func newStack(t *testing.T) *stack {
	t.Helper()
	keyPEM, err := os.ReadFile("../testdata/app-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := mockgithub.ParsePublicKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewServer(mockgithub.New(mockgithub.Config{
		AppID: appID, AppPublicKey: pub, ClientID: clientID, ClientSecret: clientSecret,
	}))
	t.Cleanup(mock.Close)

	// The app needs its own public URL for the OAuth redirect, which is only
	// known once the listener exists: start unstarted, then set the handler.
	var h http.Handler
	appSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	appSrv.Start()
	t.Cleanup(appSrv.Close)

	handler, closer, err := app.New(app.Config{
		DBPath:        filepath.Join(t.TempDir(), "ft.db"),
		GitHubAPIURL:  mock.URL,
		GitHubWebURL:  mock.URL,
		AppID:         appID,
		PrivateKeyPEM: keyPEM,
		WebhookSecret: webhookSecret,
		OAuthClientID: clientID, OAuthClientSecret: clientSecret,
		PublicURL:     appSrv.URL,
		SessionSecret: strings.Repeat("s", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	h = handler
	return &stack{app: appSrv, mock: mock, ctl: mockgithub.NewClient(mock.URL)}
}

func TestHealthz(t *testing.T) {
	s := newStack(t)
	resp, err := http.Get(s.app.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWebhookProducesCheckRun(t *testing.T) {
	s := newStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	if err := s.ctl.SeedInstallation(ctx, mockgithub.Installation{ID: 1001, AccountLogin: "acme", AccountID: 3001, AccountType: "Organization"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ctl.SeedRepo(ctx, mockgithub.Repo{InstallationID: 1001, ID: 2001, Owner: "acme", Name: "widgets", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	snd := webhooks.NewSender(s.app.URL+"/webhooks/github", []byte(webhookSecret))
	p := webhooks.Params{PRNumber: 7, HeadSHA: sha}
	for _, d := range []webhooks.Delivery{webhooks.InstallationCreated(p), webhooks.PullRequestOpened(p)} {
		r, err := snd.Send(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status/100 != 2 {
			t.Fatalf("%s: status %d: %s", d.Event, r.Status, r.Body)
		}
	}
	cr, err := s.ctl.WaitForCheckRun(ctx, "acme", "widgets", sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Name != "frozen-throne" {
		t.Fatalf("check run name %q", cr.Name)
	}
}

func TestOAuthLoginFlow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	if _, err := s.ctl.SeedUser(ctx, mockgithub.User{ID: 55, Login: "octo", Name: "Octo Cat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ctl.SetDefaultUser(ctx, "octo"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	// Follow redirects automatically; the jar keeps the state and session
	// cookies and every hop is on a test server reachable by URL.
	resp, err := c.Get(s.app.URL + "/api/v1/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = c.Get(s.app.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("me: status %d", resp.StatusCode)
	}
	var me struct{ Login string }
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me.Login != "octo" {
		t.Fatalf("login %q", me.Login)
	}
}
