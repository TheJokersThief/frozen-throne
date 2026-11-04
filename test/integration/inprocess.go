//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

// startInProcess runs the mock on an httptest server in this process and the
// real cmd/frozen-throne binary (built with the Go toolchain) as a child
// process, configured exactly like the compose app service. "In process" thus
// only means "no Docker": the app is the same binary the image ships.
func startInProcess() (*Stack, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(root, "test", "testdata", "app-key.pem")
	keyPEM, err := os.ReadFile(keyPath)
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

	dir, err := os.MkdirTemp("", "ft-it-*")
	if err != nil {
		s.Close()
		return nil, err
	}
	s.closers = append(s.closers, func() { _ = os.RemoveAll(dir) })

	bin := filepath.Join(dir, "frozen-throne")
	build := exec.Command("go", "build", "-o", bin, "./cmd/frozen-throne")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		s.Close()
		return nil, fmt.Errorf("integration: build cmd/frozen-throne: %w\n%s", err, out)
	}

	port, err := freePort()
	if err != nil {
		s.Close()
		return nil, err
	}
	appURL := "http://127.0.0.1:" + strconv.Itoa(port)
	logs := &syncBuffer{}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"FT_LISTEN_ADDR=127.0.0.1:"+strconv.Itoa(port),
		"FT_DB_PATH="+filepath.Join(dir, "frozen-throne.db"),
		"FT_GITHUB_API_URL="+mock.URL,
		"FT_GITHUB_WEB_URL="+mock.URL,
		"FT_GITHUB_APP_ID="+strconv.FormatInt(AppID, 10),
		"FT_GITHUB_PRIVATE_KEY_FILE="+keyPath,
		"FT_GITHUB_WEBHOOK_SECRET="+WebhookSecret,
		"FT_GITHUB_CLIENT_ID="+OAuthClientID,
		"FT_GITHUB_CLIENT_SECRET="+OAuthSecret,
		"FT_BASE_URL="+appURL,
		"FT_SESSION_SECRET="+SessionSecret,
		"FT_RECONCILE_INTERVAL="+ReconcileInterval,
		"FT_SLACK_ALLOW_ANY_HOST=true",
	)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		s.Close()
		return nil, fmt.Errorf("integration: start app: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	s.closers = append(s.closers, func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	s.dumpLogs = func(w *os.File) {
		fmt.Fprintln(w, "---- app process output ----")
		_, _ = w.WriteString(logs.String())
		fmt.Fprintln(w, "---- end app output ----")
	}
	s.AppURL, s.MockURL = appURL, strings.TrimRight(mock.URL, "/")
	s.MockInternalURL = s.MockURL
	return s, nil
}

// syncBuffer is a bytes.Buffer safe for the child's two output streams.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
