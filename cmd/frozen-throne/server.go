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

// buildServer wires the API, webhook receiver, reconciler and dashboard into
// one handler served from a single origin.
func buildServer(cfg config.Config, st *store.SQLite, ui fs.FS, logger *slog.Logger) (http.Handler, *reconcile.Reconciler, error) {
	key, err := github.ParsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("FT_GITHUB_PRIVATE_KEY: %w", err)
	}
	gh, err := github.NewClient(github.Config{AppID: cfg.AppID, PrivateKey: key})
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
	srv, err := api.New(api.Config{SessionSecret: cfg.SessionSecret, SecureCookies: cfg.SecureCookies, Logger: logger}, api.Deps{
		Store:  st,
		Engine: eng,
		OAuth: &api.GitHubOAuth{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL(),
		},
		Permissions: &api.GitHubPermissions{},
		Notifier:    triggerNotifier{rec},
	})
	if err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(webhookPath, svc)
	mux.Handle("/api/", srv.Handler())
	mux.Handle("/healthz", srv.Handler())
	mux.Handle("/", webui.Handler(ui))
	return mux, rec, nil
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

	h, rec, err := buildServer(cfg, st, webui.Embedded(), logger)
	if err != nil {
		return err
	}
	go func() {
		if err := rec.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("reconciler stopped", "err", err)
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
