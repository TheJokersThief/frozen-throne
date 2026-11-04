//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// startCompose brings up test/docker-compose.yml under a unique project name
// and host ports, so concurrent runs and leftovers cannot collide.
//
// Networking: the app container reaches the mock as http://mockgithub:8080
// (compose DNS), which is what it puts into OAuth authorize redirects. From the
// host the mock is on 127.0.0.1:MOCK_PORT, so the stack registers
// HostRewrites["mockgithub:8080"] and Login rewrites that host on redirects.
// The app's own URL (FT_PUBLIC_URL) is http://localhost:APP_PORT in compose, and
// AppURL uses the same host so the OAuth state cookie matches the callback.
func startCompose(ctx context.Context) (*Stack, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	composeFile := filepath.Join(root, "test", "docker-compose.yml")
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	project := "ft-it-" + hex.EncodeToString(rnd[:])

	mockPort, err := freePort()
	if err != nil {
		return nil, err
	}
	appPort, err := freePort()
	if err != nil {
		return nil, err
	}
	env := append(os.Environ(),
		"MOCK_PORT="+strconv.Itoa(mockPort),
		"APP_PORT="+strconv.Itoa(appPort),
	)
	compose := func(ctx context.Context, args ...string) *exec.Cmd {
		full := append([]string{"compose", "-f", composeFile, "-p", project}, args...)
		cmd := exec.CommandContext(ctx, "docker", full...)
		cmd.Dir = root
		cmd.Env = env
		return cmd
	}

	s := &Stack{
		webhookSecret: WebhookSecret,
		AppURL:        "http://localhost:" + strconv.Itoa(appPort),
		MockURL:       "http://127.0.0.1:" + strconv.Itoa(mockPort),
	}
	s.HostRewrites = map[string]string{ComposeMockWeb: s.MockURL}
	s.MockInternalURL = "http://" + ComposeMockWeb
	s.dumpLogs = func(w *os.File) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := compose(ctx, "logs", "--no-color", "--timestamps")
		cmd.Stdout, cmd.Stderr = w, w
		fmt.Fprintf(w, "---- docker compose logs (%s) ----\n", project)
		_ = cmd.Run()
		fmt.Fprintln(w, "---- end compose logs ----")
	}
	// Registered before `up` so a half-started project is removed as well.
	s.closers = append(s.closers, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := compose(ctx, "down", "-v", "--remove-orphans", "--rmi", "local", "--timeout", "5").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "integration: compose down: %v\n%s\n", err, out)
		}
	})

	fmt.Fprintf(os.Stderr, "integration: compose project %s (app :%d, mock :%d); building images, this can take a while\n", project, appPort, mockPort)
	upCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Stream progress to stderr while keeping a copy for the failure message.
	var captured bytes.Buffer
	up := compose(upCtx, "up", "-d", "--build", "--wait", "--wait-timeout", "180")
	up.Stdout = io.MultiWriter(os.Stderr, &captured)
	up.Stderr = up.Stdout
	if err := up.Run(); err != nil {
		s.DumpLogs(os.Stderr)
		s.Close()
		return nil, fmt.Errorf("integration: docker compose up: %w\n%s", err, tail(captured.String(), 4000))
	}
	return s, nil
}

// freePort asks the kernel for an unused TCP port on loopback. There is a small
// window between closing the probe and compose binding it; collisions surface
// as a failed `up`.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tail returns at most the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
