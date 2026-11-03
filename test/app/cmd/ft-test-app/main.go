// Command ft-test-app runs the integration-harness app (see package app).
//
// Environment:
//
//	FT_LISTEN                 listen address (default :8080)
//	FT_DB_PATH                SQLite path (required)
//	FT_GITHUB_API_URL         mock REST base (required)
//	FT_GITHUB_WEB_URL         mock OAuth web base (required)
//	FT_APP_ID                 GitHub App id (required)
//	FT_APP_PRIVATE_KEY_FILE   App private key PEM (required)
//	FT_WEBHOOK_SECRET         webhook secret (required)
//	FT_OAUTH_CLIENT_ID        OAuth client id
//	FT_OAUTH_CLIENT_SECRET    OAuth client secret
//	FT_PUBLIC_URL             externally visible base URL
//	FT_SESSION_SECRET         cookie secret, 32+ bytes (required)
//
// With -healthcheck it GETs its own /healthz and exits 0 or 1, for container
// healthchecks in images without curl.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/app"
)

func main() {
	hc := flag.Bool("healthcheck", false, "probe /healthz on FT_LISTEN and exit")
	flag.Parse()
	listen := os.Getenv("FT_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	if *hc {
		os.Exit(healthcheck(listen))
	}
	if err := run(listen); err != nil {
		log.Fatalf("ft-test-app: %v", err)
	}
}

func healthcheck(listen string) int {
	addr := listen
	if len(addr) > 0 && addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(listen string) error {
	appID, err := strconv.ParseInt(os.Getenv("FT_APP_ID"), 10, 64)
	if err != nil || appID == 0 {
		return errors.New("FT_APP_ID must be a non-zero integer")
	}
	keyFile := os.Getenv("FT_APP_PRIVATE_KEY_FILE")
	if keyFile == "" {
		return errors.New("FT_APP_PRIVATE_KEY_FILE is required")
	}
	pemBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	h, closer, err := app.New(app.Config{
		DBPath:            os.Getenv("FT_DB_PATH"),
		GitHubAPIURL:      os.Getenv("FT_GITHUB_API_URL"),
		GitHubWebURL:      os.Getenv("FT_GITHUB_WEB_URL"),
		AppID:             appID,
		PrivateKeyPEM:     pemBytes,
		WebhookSecret:     os.Getenv("FT_WEBHOOK_SECRET"),
		OAuthClientID:     os.Getenv("FT_OAUTH_CLIENT_ID"),
		OAuthClientSecret: os.Getenv("FT_OAUTH_CLIENT_SECRET"),
		PublicURL:         os.Getenv("FT_PUBLIC_URL"),
		SessionSecret:     os.Getenv("FT_SESSION_SECRET"),
	})
	if err != nil {
		return err
	}
	defer closer.Close()

	srv := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("ft-test-app listening on %s", listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
