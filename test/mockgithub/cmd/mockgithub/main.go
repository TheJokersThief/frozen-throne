// Command mockgithub runs the fake GitHub used by the integration harness.
//
// Environment:
//
//	MOCK_LISTEN               listen address (default :8080)
//	MOCK_APP_ID               GitHub App id (required)
//	MOCK_APP_PUBLIC_KEY_FILE  PEM public key, or the private key PEM (required)
//	MOCK_CLIENT_ID            OAuth client id (optional)
//	MOCK_CLIENT_SECRET        OAuth client secret (optional)
//
// GET /healthz answers 200 (not part of the mocked GitHub surface). With
// -healthcheck the binary probes its own /healthz and exits 0 or 1, for
// container healthchecks in images without curl.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func main() {
	hc := flag.Bool("healthcheck", false, "probe /healthz on MOCK_LISTEN and exit")
	flag.Parse()
	if *hc {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		log.Fatalf("mockgithub: %v", err)
	}
}

func listenAddr() string {
	if l := os.Getenv("MOCK_LISTEN"); l != "" {
		return l
	}
	return ":8080"
}

func healthcheck() int {
	addr := listenAddr()
	if addr[0] == ':' {
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

func run() error {
	appID, err := strconv.ParseInt(os.Getenv("MOCK_APP_ID"), 10, 64)
	if err != nil || appID == 0 {
		return errors.New("MOCK_APP_ID must be a non-zero integer")
	}
	keyFile := os.Getenv("MOCK_APP_PUBLIC_KEY_FILE")
	if keyFile == "" {
		return errors.New("MOCK_APP_PUBLIC_KEY_FILE is required")
	}
	pemBytes, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	pub, err := mockgithub.ParsePublicKey(pemBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", keyFile, err)
	}
	listen := listenAddr()
	gh, err := mockgithub.New(mockgithub.Config{
		AppID:        appID,
		AppPublicKey: pub,
		ClientID:     os.Getenv("MOCK_CLIENT_ID"),
		ClientSecret: os.Getenv("MOCK_CLIENT_SECRET"),
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", gh)
	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("mockgithub listening on %s (app id %d)", listen, appID)
	return srv.ListenAndServe()
}
