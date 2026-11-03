//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// checkRunCalls counts requests the app made to the mock's check-run endpoints.
func checkRunCalls(t *testing.T, e *Env) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	calls, err := e.S.Mock.Calls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range calls {
		if strings.Contains(c.Path, "/check-runs") {
			n++
		}
	}
	return n
}

func TestBadOrMissingSignatureIsRejected(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("head")
	e.SeedPR(1, sha)
	d := webhooks.PullRequestOpened(e.Params(1, sha))

	for name, snd := range map[string]*webhooks.Sender{
		"invalid": e.S.NewSender(webhooks.WithInvalidSignature()),
		"missing": e.S.NewSender(webhooks.WithoutSignature()),
	} {
		if r := e.TryDeliver(snd, d); r.Status != 401 {
			t.Errorf("%s signature: status %d, want 401 (%s)", name, r.Status, r.Body)
		}
	}
	if n := checkRunCalls(t, e); n != 0 {
		t.Fatalf("app called check-run endpoints %d times for rejected deliveries", n)
	}
	if runs := e.AllChecks(); len(runs) != 0 {
		t.Fatalf("check runs created: %+v", runs)
	}
}

func TestPingAndUnknownEventsAreAcknowledgedWithoutChecks(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	for _, d := range []webhooks.Delivery{
		webhooks.Ping(e.Params(1, e.SHA("x"))),
		webhooks.StarCreated(e.Params(1, e.SHA("x"))),
	} {
		r := e.TryDeliver(e.S.Webhooks, d)
		if r.Status/100 != 2 {
			t.Errorf("%s: status %d, want 2xx (%s)", d.Event, r.Status, r.Body)
		}
	}
	if n := checkRunCalls(t, e); n != 0 {
		t.Fatalf("app called check-run endpoints %d times", n)
	}
}
