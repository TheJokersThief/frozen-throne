package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/slack"
)

const testSecretPath = "/services/T0001/B0002/SECRETtoken1234"

// fakeSlack is an httptest Slack webhook.
type fakeSlack struct {
	srv    *httptest.Server
	status atomic.Int32
	hang   atomic.Bool
	mu     sync.Mutex
	msgs   []slack.Payload
	ch     chan struct{}
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{ch: make(chan struct{}, 64)}
	f.status.Store(200)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.hang.Load() {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		b, _ := io.ReadAll(r.Body)
		var p slack.Payload
		_ = json.Unmarshal(b, &p)
		f.mu.Lock()
		f.msgs = append(f.msgs, p)
		f.mu.Unlock()
		f.ch <- struct{}{}
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) url() string { return f.srv.URL + testSecretPath }

// wait returns the n-th message after waiting up to 3s for it.
func (f *fakeSlack) wait(t *testing.T) slack.Payload {
	t.Helper()
	select {
	case <-f.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no Slack message delivered")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.msgs[0]
	f.msgs = f.msgs[1:]
	return p
}

func (f *fakeSlack) none(t *testing.T) {
	t.Helper()
	select {
	case <-f.ch:
		t.Fatal("unexpected Slack message")
	case <-time.After(150 * time.Millisecond):
	}
}

// withSlack wires a real slack.Notifier pointed at f.
func withSlack(f *fakeSlack) envOpt {
	return func(t *testing.T, cfg *Config, deps *Deps) {
		n := slack.New(slack.Options{
			Store: deps.Store, AllowAnyHost: true, MaxAttempts: 2, BaseBackoff: time.Millisecond,
			Timeout: 500 * time.Millisecond,
		})
		t.Cleanup(n.Close)
		deps.Slack = n
		cfg.SlackAllowAnyHost = true
		cfg.DashboardURL = "https://ft.example"
	}
}

func (e *env) putSlack(url string, enabled bool, events ...string) {
	e.t.Helper()
	body := map[string]any{"enabled": enabled, "events": events}
	if url != "" {
		body["webhook_url"] = url
	}
	e.expect(e.do(e.login("alice"), "PUT", "/orgs/acme/slack", body), 200)
}

func TestSlackGetUnset(t *testing.T) {
	e := newEnv(t)
	var got map[string]any
	e.expect(e.do(e.login("alice"), "GET", "/orgs/acme/slack", nil), 200).decode(&got)
	if got["configured"] != false || got["enabled"] != false {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["webhook_url_masked"]; ok {
		t.Fatal("masked url must be omitted when unset")
	}
}

func TestSlackPutMasksAndNeverLeaksSecret(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	alice := e.login("alice")
	r := e.expect(e.do(alice, "PUT", "/orgs/acme/slack", map[string]any{
		"webhook_url": f.url(), "enabled": true, "channel": "#ops", "events": []string{"freeze", "thaw"},
	}), 200)
	var got struct {
		Configured bool     `json:"configured"`
		Enabled    bool     `json:"enabled"`
		Channel    string   `json:"channel"`
		Events     []string `json:"events"`
		Masked     string   `json:"webhook_url_masked"`
	}
	r.decode(&got)
	if !got.Configured || !got.Enabled || got.Channel != "#ops" || len(got.Events) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Masked != slack.MaskWebhookURL(f.url()) || got.Masked == "" {
		t.Fatalf("masked = %q", got.Masked)
	}
	g := e.expect(e.do(alice, "GET", "/orgs/acme/slack", nil), 200)
	for _, body := range []string{r.Body.String(), g.Body.String()} {
		if strings.Contains(body, "SECRET") || strings.Contains(body, testSecretPath) {
			t.Fatalf("secret leaked: %s", body)
		}
	}
	stored, err := e.st.GetSlackConfig(context.Background(), 1)
	if err != nil || stored.WebhookURL != f.url() {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}

func TestSlackPutKeepsExistingURL(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	e.putSlack(f.url(), true)
	e.putSlack("", false, "bypass")
	stored, _ := e.st.GetSlackConfig(context.Background(), 1)
	if stored.WebhookURL != f.url() || stored.Enabled || len(stored.Events) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestSlackPutValidation(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	alice := e.login("alice")
	put := func(body any) resp { return e.do(alice, "PUT", "/orgs/acme/slack", body) }
	// no URL stored and none given
	e.expect(put(map[string]any{"enabled": true}), 400)
	e.expect(put(map[string]any{"enabled": true, "webhook_url": "nope"}), 400)
	e.expect(put(map[string]any{"enabled": true, "webhook_url": "ftp://x.test/a/b"}), 400)
	e.expect(put(map[string]any{"enabled": true, "webhook_url": f.url(), "events": []string{"bogus"}}), 400)
	if _, err := e.st.GetSlackConfig(context.Background(), 1); err == nil {
		t.Fatal("nothing should be stored after invalid requests")
	}

	// Default config restricts hosts.
	strict := newEnv(t)
	r := strict.expect(strict.do(strict.login("alice"), "PUT", "/orgs/acme/slack",
		map[string]any{"enabled": true, "webhook_url": "https://evil.example" + testSecretPath}), 400)
	if strings.Contains(r.Body.String(), "SECRET") {
		t.Fatalf("error leaks URL: %s", r.Body.String())
	}
}

func TestSlackPermissions(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	e.putSlack(f.url(), true)
	body := map[string]any{"enabled": true}
	for _, tc := range []struct {
		user string
		want int
	}{{"bob", 403}, {"carol", 403}, {"dave", 404}} {
		c := e.login(tc.user)
		e.expect(e.do(c, "GET", "/orgs/acme/slack", nil), tc.want)
		e.expect(e.do(c, "PUT", "/orgs/acme/slack", body), tc.want)
		e.expect(e.do(c, "DELETE", "/orgs/acme/slack", nil), tc.want)
		e.expect(e.do(c, "POST", "/orgs/acme/slack/test", nil), tc.want)
	}
	e.expect(e.do(nil, "GET", "/orgs/acme/slack", nil), 401)
}

func TestSlackAuditHasNoURL(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	alice := e.login("alice")
	e.expect(e.do(alice, "PUT", "/orgs/acme/slack", map[string]any{
		"webhook_url": f.url(), "enabled": true, "channel": "#ops", "events": []string{"freeze"},
	}), 200)
	a := e.audit()[0]
	if a.Action != model.AuditSlackUpdate || a.ActorLogin != "alice" || a.InstallationID != 1 {
		t.Fatalf("audit = %+v", a)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(a.Details), &d); err != nil {
		t.Fatal(err)
	}
	if d["enabled"] != true || d["channel"] != "#ops" || d["events"] == nil {
		t.Fatalf("details = %s", a.Details)
	}
	for _, en := range e.audit() {
		if strings.Contains(en.Details, "SECRET") || strings.Contains(en.Reason, "SECRET") || strings.Contains(en.Details, "http") {
			t.Fatalf("audit leaks URL: %+v", en)
		}
	}
}

func TestSlackDeleteIsIdempotent(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	alice := e.login("alice")
	e.putSlack(f.url(), true)
	e.expect(e.do(alice, "DELETE", "/orgs/acme/slack", nil), 204)
	if _, err := e.st.GetSlackConfig(context.Background(), 1); err == nil {
		t.Fatal("config should be gone")
	}
	e.expect(e.do(alice, "DELETE", "/orgs/acme/slack", nil), 204)
	var got map[string]any
	e.expect(e.do(alice, "GET", "/orgs/acme/slack", nil), 200).decode(&got)
	if got["configured"] != false {
		t.Fatalf("got %v", got)
	}
	if a := e.audit()[0]; a.Action != model.AuditSlackUpdate {
		t.Fatalf("delete should be audited, got %+v", a)
	}
}

func TestSlackTestEndpoint(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	alice := e.login("alice")

	// not configured
	e.expect(e.do(alice, "POST", "/orgs/acme/slack/test", nil), 400)

	e.putSlack(f.url(), false) // disabled still sends the test
	e.expect(e.do(alice, "POST", "/orgs/acme/slack/test", nil), 204)
	if p := f.wait(t); !strings.Contains(p.Text, "acme") {
		t.Fatalf("test message = %q", p.Text)
	}

	f.status.Store(500)
	r := e.expect(e.do(alice, "POST", "/orgs/acme/slack/test", nil), 400)
	if !strings.Contains(r.Body.String(), "Slack rejected the test message") || strings.Contains(r.Body.String(), "SECRET") {
		t.Fatalf("body = %s", r.Body.String())
	}
}

func TestSlackTestWithoutSender(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do(e.login("alice"), "PUT", "/orgs/acme/slack", map[string]any{
		"enabled": true, "webhook_url": "https://hooks.slack.com/services/T/B/x",
	}), 200)
	e.expect(e.do(e.login("alice"), "POST", "/orgs/acme/slack/test", nil), 503)
}
