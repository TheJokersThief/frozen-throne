package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/TheJokersThief/frozen-throne/internal/api"
	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// Config configures the app under test.
type Config struct {
	DBPath       string // SQLite path; ":memory:" is allowed
	GitHubAPIURL string // REST base (the mock)
	GitHubWebURL string // OAuth web base (the mock)

	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret string

	OAuthClientID     string
	OAuthClientSecret string
	PublicURL         string // externally visible base; OAuth redirect is PublicURL + /api/v1/auth/callback
	SessionSecret     string // at least 32 bytes
}

// New builds the handler. It serves POST /webhooks/github, /api/v1/* and
// /healthz. The Closer releases the store.
func New(cfg Config) (http.Handler, io.Closer, error) {
	if cfg.DBPath == "" {
		return nil, nil, errors.New("app: DBPath is required")
	}
	key, err := github.ParsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (http.Handler, io.Closer, error) {
		_ = st.Close()
		return nil, nil, err
	}
	eng := engine.New()

	gh, err := github.NewClient(github.Config{AppID: cfg.AppID, PrivateKey: key, BaseURL: cfg.GitHubAPIURL})
	if err != nil {
		return fail(err)
	}
	hooks, err := github.NewService(github.ServiceConfig{
		Store: st, Engine: eng, API: gh, WebhookSecret: []byte(cfg.WebhookSecret),
	})
	if err != nil {
		return fail(err)
	}

	srv, err := api.New(api.Config{SessionSecret: []byte(cfg.SessionSecret), SecureCookies: false}, api.Deps{
		Store:  st,
		Engine: eng,
		OAuth: &api.GitHubOAuth{
			ClientID: cfg.OAuthClientID, ClientSecret: cfg.OAuthClientSecret,
			RedirectURL: strings.TrimRight(cfg.PublicURL, "/") + api.Prefix + "/auth/callback",
			WebBase:     cfg.GitHubWebURL, APIBase: cfg.GitHubAPIURL,
		},
		Permissions: &api.GitHubPermissions{APIBase: cfg.GitHubAPIURL},
		// extension point: reconcile. A real ChangeNotifier that re-evaluates
		// Check Runs after freeze changes plugs in here; nil means no-op.
		Notifier: nil,
	})
	if err != nil {
		return fail(err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/github", hooks)
	mux.Handle("/", srv.Handler())
	return mux, st, nil
}
