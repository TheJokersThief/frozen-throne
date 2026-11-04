//go:build integration

package integration

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// startExternal attaches to a stack someone else started.
//
//	FT_APP_URL            host-reachable app base URL (required)
//	FT_MOCK_URL           host-reachable mock base URL (required)
//	FT_WEBHOOK_SECRET     webhook secret (default: the compose value)
//	FT_MOCK_WEB_INTERNAL  host:port the app uses for the mock's OAuth pages if
//	                      that name is not resolvable from here (optional)
func startExternal() (*Stack, error) {
	app, mock := strings.TrimRight(os.Getenv("FT_APP_URL"), "/"), strings.TrimRight(os.Getenv("FT_MOCK_URL"), "/")
	if app == "" || mock == "" {
		return nil, errors.New("integration: external mode needs FT_APP_URL and FT_MOCK_URL")
	}
	for _, u := range []string{app, mock} {
		if p, err := url.Parse(u); err != nil || p.Host == "" {
			return nil, errors.New("integration: bad URL " + u)
		}
	}
	secret := os.Getenv("FT_WEBHOOK_SECRET")
	if secret == "" {
		secret = WebhookSecret
	}
	s := &Stack{AppURL: app, MockURL: mock, webhookSecret: secret, HostRewrites: map[string]string{}}
	s.MockInternalURL = mock
	if h := os.Getenv("FT_MOCK_WEB_INTERNAL"); h != "" {
		s.HostRewrites[h] = mock
		s.MockInternalURL = "http://" + h
	}
	return s, nil
}
