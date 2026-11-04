package mockgithub_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func TestCallsAreRecordedWithRedactedAuth(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	if _, err := h.app(t).CreateCheckRun(ctx, 7, "acme", "web", github.CheckRun{HeadSHA: "abc", Conclusion: "success", Title: "T", Summary: "S"}); err != nil {
		t.Fatal(err)
	}

	calls, err := h.ctl.Calls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 calls (token, check-run), got %+v", calls)
	}
	tokenCall, runCall := calls[0], calls[1]
	if tokenCall.Seq >= runCall.Seq {
		t.Fatalf("seq not increasing: %d, %d", tokenCall.Seq, runCall.Seq)
	}
	if tokenCall.Method != "POST" || tokenCall.Path != "/app/installations/7/access_tokens" || tokenCall.Status != 201 {
		t.Fatalf("token call %+v", tokenCall)
	}
	if got := tokenCall.Headers["Authorization"]; got != "Bearer <app-jwt>" {
		t.Fatalf("jwt auth recorded as %q", got)
	}
	if got := runCall.Headers["Authorization"]; got != "Bearer <installation-token>" {
		t.Fatalf("installation auth recorded as %q", got)
	}
	if runCall.Method != "POST" || runCall.Path != "/repos/acme/web/check-runs" || runCall.Status != 201 {
		t.Fatalf("run call %+v", runCall)
	}
	if !strings.Contains(runCall.Body, `"head_sha":"abc"`) {
		t.Fatalf("body not recorded: %q", runCall.Body)
	}
	for _, c := range calls {
		for k, v := range c.Headers {
			if strings.Contains(v, "ghs_") || strings.Count(v, ".") == 2 {
				t.Fatalf("secret leaked in header %s=%s", k, v)
			}
		}
	}
}

func TestQueryAndUserTokensAndSecretsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedAlice(t, h)
	_ = h.ctl.SetDefaultUser(ctx, "alice")
	_ = h.ctl.ClearCalls(ctx)
	o := h.oauth()
	code, _ := h.ctl.SeedUser(ctx, mockgithub.User{ID: 100, Login: "alice", Token: "gho_alice"})
	tok, err := o.Exchange(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.User(ctx, tok); err != nil {
		t.Fatal(err)
	}
	authorize(t, o.AuthorizeURL("st"))

	calls, _ := h.ctl.Calls(ctx)
	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %+v", calls)
	}
	if calls[0].Path != "/login/oauth/access_token" || strings.Contains(calls[0].Body, clientSecret) || !strings.Contains(calls[0].Body, "client_secret=%5Bredacted%5D") {
		t.Fatalf("exchange call %+v", calls[0])
	}
	if calls[1].Headers["Authorization"] != "Bearer <user-token>" || strings.Contains(calls[1].Headers["Authorization"], "gho_") {
		t.Fatalf("user call %+v", calls[1])
	}
	if calls[2].Status != http.StatusFound || !strings.Contains(calls[2].Query, "state=st") {
		t.Fatalf("authorize call %+v", calls[2])
	}
	// Control API traffic is not recorded.
	for _, c := range calls {
		if strings.HasPrefix(c.Path, "/_mock/") {
			t.Fatalf("control call recorded: %+v", c)
		}
	}
}

func TestClearCallsAndResetEmptyTheLog(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	_, _ = h.app(t).ListOpenPullRequests(ctx, 7, "acme", "web")
	if calls, _ := h.ctl.Calls(ctx); len(calls) == 0 {
		t.Fatal("expected calls")
	}
	if err := h.ctl.ClearCalls(ctx); err != nil {
		t.Fatal(err)
	}
	if calls, _ := h.ctl.Calls(ctx); len(calls) != 0 {
		t.Fatalf("after clear: %+v", calls)
	}
	_, _ = h.app(t).ListOpenPullRequests(ctx, 7, "acme", "web")
	_ = h.ctl.Reset(ctx)
	if calls, _ := h.ctl.Calls(ctx); len(calls) != 0 {
		t.Fatalf("after reset: %+v", calls)
	}
}

func TestFaultFailsNextNMatchingRequests(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	app := h.app(t)
	if err := h.ctl.AddFault(ctx, mockgithub.Fault{Method: "POST", PathPrefix: "/repos/acme/web/check-runs", Status: 503, Count: 2}); err != nil {
		t.Fatal(err)
	}
	cr := github.CheckRun{HeadSHA: "abc", Conclusion: "success"}
	for i := 0; i < 2; i++ {
		_, err := app.CreateCheckRun(ctx, 7, "acme", "web", cr)
		var he *github.HTTPError
		if !errors.As(err, &he) || he.Status != 503 {
			t.Fatalf("attempt %d: want 503, got %v", i, err)
		}
	}
	if _, err := app.CreateCheckRun(ctx, 7, "acme", "web", cr); err != nil {
		t.Fatalf("third attempt should succeed: %v", err)
	}
	if runs, _ := h.ctl.CheckRuns(ctx, "acme", "web", "abc", ""); len(runs) != 1 {
		t.Fatalf("faulted requests must not create runs, got %d", len(runs))
	}
	calls, _ := h.ctl.Calls(ctx)
	var statuses []int
	for _, c := range calls {
		if c.Path == "/repos/acme/web/check-runs" {
			statuses = append(statuses, c.Status)
		}
	}
	if len(statuses) != 3 || statuses[0] != 503 || statuses[1] != 503 || statuses[2] != 201 {
		t.Fatalf("recorded statuses %v", statuses)
	}
}

func TestFaultMethodAndPrefixScoping(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	app := h.app(t)
	_ = h.ctl.AddFault(ctx, mockgithub.Fault{Method: "POST", PathPrefix: "/repos/", Status: 500, Count: 1})
	// GET is not matched by a POST fault.
	if _, err := app.ListOpenPullRequests(ctx, 7, "acme", "web"); err != nil {
		t.Fatalf("GET should pass: %v", err)
	}
	// Token endpoint does not match the /repos/ prefix.
	_ = h.ctl.AddFault(ctx, mockgithub.Fault{PathPrefix: "/user", Status: 502, Count: 1})
	if _, err := app.ListOpenPullRequests(ctx, 7, "acme", "web"); err != nil {
		t.Fatalf("unrelated prefix: %v", err)
	}
}

func TestExpiredInstallationTokenIs401AndClientRecovers(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	app := h.app(t)
	if _, err := app.ListOpenPullRequests(ctx, 7, "acme", "web"); err != nil {
		t.Fatal(err)
	}
	// A one-shot 401 on the pulls call: the real client refreshes its token.
	_ = h.ctl.AddFault(ctx, mockgithub.Fault{Method: "GET", PathPrefix: "/repos/acme/web/pulls", Status: 401, Count: 1})
	if _, err := app.ListOpenPullRequests(ctx, 7, "acme", "web"); err != nil {
		t.Fatalf("client should retry after 401: %v", err)
	}
	calls, _ := h.ctl.Calls(ctx)
	tokens := 0
	for _, c := range calls {
		if strings.HasSuffix(c.Path, "/access_tokens") {
			tokens++
		}
	}
	if tokens != 2 {
		t.Fatalf("want 2 token mints (initial + refresh), got %d", tokens)
	}
}

func TestRecordedQueryRedactsClientSecret(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.ctl.ClearCalls(ctx)
	do(t, http.MethodGet, h.srv.URL+"/login/oauth/authorize?state=st&client_secret="+clientSecret, "", nil)
	calls, _ := h.ctl.Calls(ctx)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %+v", calls)
	}
	q := calls[0].Query
	if strings.Contains(q, clientSecret) || !strings.Contains(q, "client_secret=%5Bredacted%5D") || !strings.Contains(q, "state=st") {
		t.Fatalf("query not redacted: %q", q)
	}
}
