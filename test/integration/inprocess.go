//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/TheJokersThief/frozen-throne/test/app"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

// startInProcess runs the mock and the app on httptest servers in this
// process. The wiring and env semantics mirror docker-compose.yml: the app is
// given the mock's URL for both REST and OAuth web, and its own listener URL as
// FT_PUBLIC_URL, so OAuth redirects need no rewriting.
func startInProcess() (*Stack, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(root, "test", "testdata", "app-key.pem"))
	if err != nil {
		return nil, err
	}
	pub, err := mockgithub.ParsePublicKey(keyPEM)
	if err != nil {
		return nil, err
	}
	s := &Stack{webhookSecret: WebhookSecret, HostRewrites: map[string]string{}}

	gh, err := mockgithub.New(mockgithub.Config{
		AppID: AppID, AppPublicKey: pub, ClientID: OAuthClientID, ClientSecret: OAuthSecret,
	})
	if err != nil {
		return nil, err
	}
	mock := httptest.NewServer(gh)
	s.closers = append(s.closers, mock.Close)

	// The app's public URL is its own listener, which only exists once started:
	// start first, install the handler afterwards.
	var h http.Handler
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	s.closers = append(s.closers, appSrv.Close)

	dir, err := os.MkdirTemp("", "ft-it-*")
	if err != nil {
		s.Close()
		return nil, err
	}
	s.closers = append(s.closers, func() { _ = os.RemoveAll(dir) })

	handler, closer, err := app.New(app.Config{
		DBPath:       filepath.Join(dir, "frozen-throne.db"),
		GitHubAPIURL: mock.URL, GitHubWebURL: mock.URL,
		AppID: AppID, PrivateKeyPEM: keyPEM, WebhookSecret: WebhookSecret,
		OAuthClientID: OAuthClientID, OAuthClientSecret: OAuthSecret,
		PublicURL: appSrv.URL, SessionSecret: SessionSecret,
	})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("integration: build app: %w", err)
	}
	s.closers = append(s.closers, func() { _ = closer.Close() })
	h = handler
	s.AppURL, s.MockURL = strings.TrimRight(appSrv.URL, "/"), strings.TrimRight(mock.URL, "/")
	return s, nil
}
