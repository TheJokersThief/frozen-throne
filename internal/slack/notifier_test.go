package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type fakeStore struct {
	store.Slack
	cfg map[int64]model.SlackConfig
}

func (f *fakeStore) GetSlackConfig(_ context.Context, id int64) (model.SlackConfig, error) {
	c, ok := f.cfg[id]
	if !ok {
		return model.SlackConfig{}, model.ErrNotFound
	}
	return c, nil
}

func newStore(c model.SlackConfig) *fakeStore {
	return &fakeStore{cfg: map[int64]model.SlackConfig{c.InstallationID: c}}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type harness struct {
	n     *Notifier
	logs  *syncBuf
	fails chan error
	sleep []time.Duration
	smu   sync.Mutex
}

func newHarness(t *testing.T, cfg model.SlackConfig, mod func(*Options)) *harness {
	t.Helper()
	h := &harness{logs: &syncBuf{}, fails: make(chan error, 64)}
	o := Options{
		Store:        newStore(cfg),
		Logger:       slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		AllowAnyHost: true,
		Timeout:      2 * time.Second,
		MaxAttempts:  4,
		OnFailure:    func(_ Event, err error) { h.fails <- err },
		Sleep:        func(d time.Duration) { h.smu.Lock(); h.sleep = append(h.sleep, d); h.smu.Unlock() },
		Workers:      1,
	}
	if mod != nil {
		mod(&o)
	}
	h.n = New(o)
	t.Cleanup(h.n.Close)
	return h
}

func ev(k Kind) Event {
	return Event{InstallationID: 1, Kind: k, Org: "acme", Scope: "org", Actor: "alice", Reason: "r", DashboardURL: "https://d/x"}
}

func TestNotifyDelivers(t *testing.T) {
	got := make(chan Payload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var p Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		got <- p
	}))
	defer srv.Close()
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL + "/services/T/B/secret", Channel: "#ops", Enabled: true}, nil)
	h.n.Notify(ev(KindFreeze))
	select {
	case p := <-got:
		if p.Channel != "#ops" || !strings.Contains(p.Text, "acme") {
			t.Fatalf("payload %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
}

func TestNotifySkips(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cases := map[string]struct {
		cfg model.SlackConfig
		ev  Event
	}{
		"disabled":     {model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL, Enabled: false}, ev(KindFreeze)},
		"filtered":     {model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL, Enabled: true, Events: []string{"thaw"}}, ev(KindFreeze)},
		"unconfigured": {model.SlackConfig{InstallationID: 99, WebhookURL: srv.URL, Enabled: true}, ev(KindFreeze)},
	}
	for name, c := range cases {
		h := newHarness(t, c.cfg, nil)
		h.n.Notify(c.ev)
		h.n.Close() // drains
		if hits.Load() != 0 {
			t.Fatalf("%s: unexpected delivery", name)
		}
		if len(h.fails) != 0 {
			t.Fatalf("%s: skip must not be a failure", name)
		}
	}
}

func TestRetryThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			w.WriteHeader(500)
		case 2:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		}
	}))
	defer srv.Close()
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL, Enabled: true}, nil)
	h.n.Notify(ev(KindThaw))
	h.n.Close()
	if hits.Load() != 3 {
		t.Fatalf("attempts = %d", hits.Load())
	}
	if len(h.fails) != 0 {
		t.Fatal("unexpected failure")
	}
	h.smu.Lock()
	defer h.smu.Unlock()
	if len(h.sleep) != 2 || h.sleep[1] != time.Second {
		t.Fatalf("sleeps %v", h.sleep)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(400) }))
	defer srv.Close()
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL + "/services/T/B/topsecret", Enabled: true}, nil)
	h.n.Notify(ev(KindThaw))
	h.n.Close()
	if hits.Load() != 1 {
		t.Fatalf("attempts = %d", hits.Load())
	}
	if len(h.fails) != 1 {
		t.Fatal("OnFailure not called")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(503) }))
	defer srv.Close()
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL, Enabled: true}, func(o *Options) { o.MaxAttempts = 3 })
	h.n.Notify(ev(KindThaw))
	h.n.Close()
	if hits.Load() != 3 {
		t.Fatalf("attempts = %d", hits.Load())
	}
	if len(h.fails) != 1 {
		t.Fatal("OnFailure not called once")
	}
}

func TestPerAttemptTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL + "/services/T/B/hangsecret", Enabled: true},
		func(o *Options) { o.Timeout = 50 * time.Millisecond; o.MaxAttempts = 2 })
	h.n.Notify(ev(KindThaw))
	select {
	case err := <-h.fails:
		if strings.Contains(err.Error(), "hangsecret") {
			t.Fatalf("error leaks url: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout not enforced")
	}
	if strings.Contains(h.logs.String(), "hangsecret") {
		t.Fatalf("logs leak url: %s", h.logs.String())
	}
}

func TestNotifyNeverBlocksAndDropsWhenFull(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL, Enabled: true},
		func(o *Options) { o.Queue = 2; o.Timeout = 10 * time.Second })
	start := time.Now()
	for i := 0; i < 20; i++ {
		h.n.Notify(ev(KindFreeze))
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Notify blocked for %v", d)
	}
	drops := 0
	for len(h.fails) > 0 {
		if errors.Is(<-h.fails, ErrQueueFull) {
			drops++
		}
	}
	if drops < 15 {
		t.Fatalf("drops = %d", drops)
	}
	if !strings.Contains(h.logs.String(), "queue full") {
		t.Fatal("no warn log")
	}
}

func TestCloseTwiceAndNotifyAfterClose(t *testing.T) {
	h := newHarness(t, model.SlackConfig{InstallationID: 1}, nil)
	h.n.Close()
	h.n.Close()
	h.n.Notify(ev(KindFreeze)) // must not panic
}

func TestSendTest(t *testing.T) {
	var mu sync.Mutex
	var body Payload
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &body)
		s := status
		mu.Unlock()
		w.WriteHeader(s)
	}))
	defer srv.Close()
	cfg := model.SlackConfig{InstallationID: 1, WebhookURL: srv.URL + "/services/T/B/sendsecret", Channel: "#c", Enabled: false, Events: []string{"thaw"}}
	h := newHarness(t, cfg, nil)
	if err := h.n.SendTest(context.Background(), 1, "acme", "https://d/x"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !strings.Contains(body.Text, "acme") || !strings.Contains(body.Text, "Open dashboard") || body.Channel != "#c" {
		t.Fatalf("body %+v", body)
	}
	status = 500
	mu.Unlock()
	err := h.n.SendTest(context.Background(), 1, "acme", "")
	if err == nil || strings.Contains(err.Error(), "sendsecret") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err = %v", err)
	}
	if err := h.n.SendTest(context.Background(), 2, "acme", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
	h2 := newHarness(t, model.SlackConfig{InstallationID: 1}, nil)
	if err := h2.n.SendTest(context.Background(), 1, "acme", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty url: %v", err)
	}
}

func TestNetworkErrorDoesNotLeakURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/services/T/B/netsecret"
	srv.Close() // connection refused
	h := newHarness(t, model.SlackConfig{InstallationID: 1, WebhookURL: url, Enabled: true}, func(o *Options) { o.MaxAttempts = 2 })
	h.n.Notify(ev(KindFreeze))
	h.n.Close()
	err := <-h.fails
	if strings.Contains(err.Error(), "netsecret") || strings.Contains(h.logs.String(), "netsecret") {
		t.Fatalf("leak: %v / %s", err, h.logs.String())
	}
	err = h.n.SendTest(context.Background(), 1, "acme", "")
	if err == nil || strings.Contains(err.Error(), "netsecret") {
		t.Fatalf("SendTest err: %v", err)
	}
}
