package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TheJokersThief/frozen-throne/internal/config"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		BaseURL: "http://localhost:8080", AppID: 1, PrivateKeyPEM: pemKey, WebhookSecret: []byte("s"),
		ClientID: "id", ClientSecret: "sec", SessionSecret: []byte(strings.Repeat("x", 32)),
	}
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>dashboard</html>")}}
	h, _, err := buildServer(cfg, st, ui, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestBuildServerRoutes(t *testing.T) {
	h := testHandler(t)
	if r := do(h, "GET", "/healthz"); r.Code != 200 || !strings.Contains(r.Body.String(), "ok") {
		t.Errorf("/healthz: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/"); r.Code != 200 || !strings.Contains(r.Body.String(), "dashboard") {
		t.Errorf("/: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/repos/acme/web"); r.Code != 200 || !strings.Contains(r.Body.String(), "dashboard") {
		t.Errorf("spa: %d", r.Code)
	}
	if r := do(h, "GET", "/api/v1/auth/me"); r.Code != 401 {
		t.Errorf("/api/v1/auth/me unauthenticated: %d", r.Code)
	}
	if r := do(h, "POST", webhookPath); r.Code != 401 {
		t.Errorf("unsigned webhook: %d", r.Code)
	}
}

func TestBuildServerRejectsBadKey(t *testing.T) {
	st, _ := store.Open(context.Background(), ":memory:")
	defer st.Close()
	_, _, err := buildServer(config.Config{PrivateKeyPEM: []byte("nope")}, st, fstest.MapFS{}, slog.Default())
	if err == nil {
		t.Fatal("expected error")
	}
}
