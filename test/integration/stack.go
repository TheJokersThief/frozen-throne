//go:build integration

// Package integration runs frozen-throne against a mock GitHub, driven by
// signed webhooks and real OAuth logins. See test/README.md.
package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// Fixed throwaway values shared by every mode. They match
// test/docker-compose.yml and must stay in sync with it.
const (
	AppID         int64 = 424242
	WebhookSecret       = "whsec_integration_test"
	OAuthClientID       = "Iv1.testclient"
	OAuthSecret         = "testclientsecret"
	SessionSecret       = "integration-test-session-secret-32b!"

	// ComposeMockWeb is the host:port the app container uses for the mock
	// (FT_GITHUB_WEB_URL=http://mockgithub:8080). It is not resolvable from
	// the host, so Login rewrites it (see Stack.HostRewrites).
	ComposeMockWeb = "mockgithub:8080"

	// ReconcileInterval is the app's periodic full re-check (FT_RECONCILE_INTERVAL).
	// Short so "missed webhook heals" scenarios finish quickly; same value as
	// docker-compose.yml.
	ReconcileInterval = "2s"
)

// Modes selected by INTEGRATION_MODE.
const (
	ModeAuto      = "auto"
	ModeCompose   = "compose"
	ModeInProcess = "inprocess"
	ModeExternal  = "external"
)

// Stack is a running app plus the mock GitHub it talks to. It is created once
// in TestMain and shared; tests are serial and call Reset first.
type Stack struct {
	Mode    string
	AppURL  string // host-reachable base URL of the app
	MockURL string // host-reachable base URL of the mock
	// MockInternalURL is how the app reaches the mock (http://mockgithub:8080 in
	// compose). Used for URLs the app must call back, such as the fake Slack.
	MockInternalURL string
	Mock            *mockgithub.Client
	Webhooks        *webhooks.Sender // correctly signed; see NewSender for variants

	// HostRewrites maps a host[:port] that appears in redirects issued by the
	// app or the mock but is only resolvable inside the stack (e.g. the compose
	// DNS name "mockgithub:8080") to the base URL to use from the host.
	HostRewrites map[string]string

	webhookSecret string
	closers       []func()
	dumpLogs      func(w *os.File) // compose only
	resetOnce     sync.Once
	seq           int
}

// Start builds the stack for the mode in INTEGRATION_MODE. Progress is logged
// to stderr because TestMain has no *testing.T.
func Start(ctx context.Context) (*Stack, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("INTEGRATION_MODE")))
	if mode == "" {
		mode = ModeAuto
	}
	switch mode {
	case ModeAuto:
		if dockerAvailable(ctx) {
			mode = ModeCompose
		} else {
			mode = ModeInProcess
		}
		fmt.Fprintf(os.Stderr, "integration: INTEGRATION_MODE=auto selected %q\n", mode)
	case ModeCompose, ModeInProcess, ModeExternal:
	default:
		return nil, fmt.Errorf("integration: unknown INTEGRATION_MODE %q (want auto, compose, inprocess or external)", mode)
	}

	var (
		s   *Stack
		err error
	)
	switch mode {
	case ModeCompose:
		s, err = startCompose(ctx)
	case ModeInProcess:
		s, err = startInProcess()
	case ModeExternal:
		s, err = startExternal()
	}
	if err != nil {
		return nil, err
	}
	s.Mode = mode
	s.Mock = mockgithub.NewClient(s.MockURL)
	s.Webhooks = s.NewSender()
	if err := s.waitReady(ctx); err != nil {
		s.DumpLogs(os.Stderr)
		s.Close()
		return nil, err
	}
	return s, nil
}

// NewSender returns a webhook sender aimed at the app, signing with the stack's
// secret unless opts say otherwise (webhooks.WithoutSignature, ...).
func (s *Stack) NewSender(opts ...webhooks.Option) *webhooks.Sender {
	opts = append([]webhooks.Option{webhooks.WithTargetID(AppID)}, opts...)
	return webhooks.NewSender(s.AppURL+"/webhooks/github", []byte(s.webhookSecret), opts...)
}

// Close tears the stack down. Safe to call once.
func (s *Stack) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	s.closers = nil
}

// DumpLogs writes container logs (compose mode) to f; a no-op otherwise.
func (s *Stack) DumpLogs(f *os.File) {
	if s.dumpLogs != nil {
		s.dumpLogs(f)
	}
}

func (s *Stack) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, u := range []string{s.AppURL + "/healthz", s.MockURL + "/_mock/calls"} {
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("integration: %s never became ready: %w", u, ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

// Reset prepares a clean slate for one test: it clears the mock (state, calls,
// faults) and seeds a fresh Env with ids no earlier test used, so rows the app
// keeps in its own database from earlier tests cannot collide with this one.
func (s *Stack) Reset(t *testing.T) *Env {
	t.Helper()
	s.resetOnce.Do(func() { t.Logf("integration mode: %s (app %s, mock %s)", s.Mode, s.AppURL, s.MockURL) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Mock.Reset(ctx); err != nil {
		t.Fatalf("reset mock: %v", err)
	}
	s.seq++
	return newEnv(t, s, s.seq)
}

// repoRoot finds the module root (the directory holding go.mod).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("integration: go.mod not found above the working directory")
		}
		dir = parent
	}
}

func dockerAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
}
