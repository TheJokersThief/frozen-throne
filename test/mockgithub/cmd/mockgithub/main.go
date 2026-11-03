// Command mockgithub runs the fake GitHub used by the integration harness.
//
// Environment:
//
//	MOCK_LISTEN               listen address (default :8080)
//	MOCK_APP_ID               GitHub App id (required)
//	MOCK_APP_PUBLIC_KEY_FILE  PEM public key, or the private key PEM (required)
//	MOCK_CLIENT_ID            OAuth client id (optional)
//	MOCK_CLIENT_SECRET        OAuth client secret (optional)
package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("mockgithub: %v", err)
	}
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
	listen := os.Getenv("MOCK_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	srv := &http.Server{
		Addr: listen,
		Handler: mockgithub.New(mockgithub.Config{
			AppID:        appID,
			AppPublicKey: pub,
			ClientID:     os.Getenv("MOCK_CLIENT_ID"),
			ClientSecret: os.Getenv("MOCK_CLIENT_SECRET"),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("mockgithub listening on %s (app id %d)", listen, appID)
	return srv.ListenAndServe()
}
