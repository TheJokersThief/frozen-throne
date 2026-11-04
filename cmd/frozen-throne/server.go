package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/api"
	"github.com/TheJokersThief/frozen-throne/internal/config"
	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/reconcile"
	"github.com/TheJokersThief/frozen-throne/internal/slack"
	"github.com/TheJokersThief/frozen-throne/internal/store"
	"github.com/TheJokersThief/frozen-throne/internal/webui"
)

// webhookPath is where the GitHub App delivers events.
const webhookPath = "/webhooks/github"

// triggerNotifier turns committed freeze changes into reconcile triggers.
type triggerNotifier struct{ t reconcile.Trigger }

func (n triggerNotifier) FreezeStateChanged(_ context.Context, ev api.FreezeChange) {
	if ev.RepoID != nil {
		n.t.Repo(*ev.RepoID)
		return
	}
	n.t.Installation(ev.InstallationID)
}

// background is the work that runs beside the HTTP handler: the reconciler
// and the Slack window watcher, plus the Slack delivery queue.
type background struct {
	rec     *reconcile.Reconciler
	watcher *slack.WindowWatcher
	slack   *slack.Notifier
}

// Run blocks until ctx is cancelled.
func (b *background) Run(ctx context.Context) error {
	go b.watcher.Run(ctx)
	return b.rec.Run(ctx)
}

// Close drains pending Slack notifications.
func (b *background) Close() { b.slack.Close() }

// buildServer wires the API, webhook receiver, reconciler and dashboard into
// one handler served from a single origin.
func buildServer(cfg config.Config, st *store.SQLite, ui fs.FS, logger *slog.Logger) (http.Handler, *background, error) {
	key, err := github.ParsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("FT_GITHUB_PRIVATE_KEY: %w", err)
	}
	gh, err := github.NewClient(github.Config{AppID: cfg.AppID, PrivateKey: key, BaseURL: cfg.GitHubAPIURL})
	if err != nil {
		return nil, nil, err
	}
	eng := engine.New()
	svc, err := github.NewService(github.ServiceConfig{Store: st, Engine: eng, API: gh, WebhookSecret: cfg.WebhookSecret, Logger: logger})
	if err != nil {
		return nil, nil, err
	}
	rec, err := newReconciler(st, svc, logger, cfg.ReconcileInterval)
	if err != nil {
		return nil, nil, err
	}
	notifier := slack.New(slack.Options{Store: st, Logger: logger, AllowAnyHost: cfg.SlackAllowAnyHost})
	watcher := slack.NewWindowWatcher(slack.WindowOptions{Store: st, Notifier: notifier, DashboardURL: cfg.BaseURL, Logger: logger})
	srv, err := api.New(api.Config{
		SessionSecret: cfg.SessionSecret, SecureCookies: cfg.SecureCookies, Logger: logger,
		DashboardURL: cfg.BaseURL, SlackAllowAnyHost: cfg.SlackAllowAnyHost,
	}, api.Deps{
		Store:  st,
		Engine: eng,
		OAuth: &api.GitHubOAuth{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL(),
			WebBase: cfg.GitHubWebURL, APIBase: cfg.GitHubAPIURL,
		},
		Permissions: &api.GitHubPermissions{APIBase: cfg.GitHubAPIURL},
		Notifier:    triggerNotifier{rec},
		Slack:       notifier,
	})
	if err != nil {
		notifier.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(webhookPath, svc)
	mux.Handle("/api/", srv.Handler())
	mux.Handle("/healthz", srv.Handler())
	mux.Handle("/", webui.Handler(ui))
	return mux, &background{rec: rec, watcher: watcher, slack: notifier}, nil
}

// serve runs the server until SIGINT/SIGTERM.
func serve() error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Getenv, os.ReadFile)
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBPath, err)
	}
	defer st.Close()

	h, bg, err := buildServer(cfg, st, webui.Embedded(), logger)
	if err != nil {
		return err
	}
	defer bg.Close()
	go func() {
		if err := bg.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("background workers stopped", "err", err)
		}
	}()

	hs := &http.Server{Addr: cfg.ListenAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	logger.Info("frozen-throne listening", "addr", cfg.ListenAddr, "base_url", cfg.BaseURL, "version", version)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
