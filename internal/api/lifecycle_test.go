package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/reconcile"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// ---- fakes: GitHub and clock ----

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type ghCheck struct {
	sha, conclusion, summary string
}

// lifeGH records Check Runs; check() reports the latest write for a head SHA
// (a webhook and the reconciler may both create a run for the same SHA).
type lifeGH struct {
	mu     sync.Mutex
	prs    []github.PullSummary
	lists  int
	checks map[int64]*ghCheck
	latest map[string]ghCheck
	next   int64
}

func newLifeGH() *lifeGH {
	return &lifeGH{checks: map[int64]*ghCheck{}, latest: map[string]ghCheck{}}
}

func sha(pr int, rev int) string { return fmt.Sprintf("sha-%d-%d", pr, rev) }

func (g *lifeGH) CreateCheckRun(_ context.Context, _ int64, _, _ string, c github.CheckRun) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	g.checks[g.next] = &ghCheck{c.HeadSHA, c.Conclusion, c.Summary}
	g.latest[c.HeadSHA] = ghCheck{c.HeadSHA, c.Conclusion, c.Summary}
	return g.next, nil
}

func (g *lifeGH) UpdateCheckRun(_ context.Context, _ int64, _, _ string, id int64, c github.CheckRun) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	ck, ok := g.checks[id]
	if !ok {
		return &github.HTTPError{Status: 404}
	}
	ck.conclusion, ck.summary = c.Conclusion, c.Summary
	g.latest[ck.sha] = *ck
	return nil
}

func (g *lifeGH) ListOpenPullRequests(context.Context, int64, string, string) ([]github.PullSummary, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lists++
	return append([]github.PullSummary(nil), g.prs...), nil
}

func (g *lifeGH) setPRs(prs ...github.PullSummary) {
	g.mu.Lock()
	g.prs = prs
	g.mu.Unlock()
}

func (g *lifeGH) check(sha string) ghCheck {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.latest[sha]
}

// ---- wiring: API -> store -> engine -> reconcile -> fake GitHub ----

const hookSecret = "hook-secret"

type life struct {
	*env
	clk *testClock
	gh  *lifeGH
	svc *github.Service
}

func newLife(t *testing.T) *life {
	t.Helper()
	l := &life{clk: &testClock{t: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}, gh: newLifeGH()}
	l.env = newEnv(t, func(t *testing.T, cfg *Config, d *Deps) {
		cfg.Now = l.clk.Now
		d.Engine = engine.New()
		svc, err := github.NewService(github.ServiceConfig{
			Store: d.Store, Engine: d.Engine, API: l.gh, WebhookSecret: []byte(hookSecret), Now: l.clk.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		l.svc = svc
		rc, err := reconcile.New(reconcile.Config{Store: d.Store, Reevaluator: svc, Engine: d.Engine, Interval: -1})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- rc.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
		d.Notifier = NewTriggerNotifier(rc)
	})
	// Let the reconciler's startup pass finish (it sees no PRs) so it cannot
	// race the first webhook into creating a duplicate Check Run.
	l.eventually("startup pass", func() bool { l.gh.mu.Lock(); defer l.gh.mu.Unlock(); return l.gh.lists > 0 })
	return l
}

func (l *life) eventually(what string, f func() bool) {
	l.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	prs, _ := l.st.ListOpenPullRequests(context.Background(), 10)
	by, _ := l.st.ListBypasses(context.Background(), store.BypassFilter{RepoID: ptr(int64(10))})
	l.t.Fatalf("timed out waiting for %s; open=%+v bypasses=%+v", what, prs, by)
}

func (l *life) wantCheck(sha, conclusion string) ghCheck {
	l.t.Helper()
	l.eventually(fmt.Sprintf("check %s = %s", sha, conclusion), func() bool { return l.gh.check(sha).conclusion == conclusion })
	return l.gh.check(sha)
}

func (l *life) webhook(action string, pr int, headSHA string, merged bool) {
	l.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"action":       action,
		"installation": map[string]any{"id": 1, "account": map[string]any{"login": "acme", "id": 100, "type": "Organization"}},
		"repository":   map[string]any{"id": 10, "name": "web", "owner": map[string]any{"login": "acme"}, "default_branch": "main"},
		"pull_request": map[string]any{"number": pr, "merged": merged, "head": map[string]any{"sha": headSHA}},
	})
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	l.svc.ServeHTTP(rec, req)
	if rec.Code != 204 {
		l.t.Fatalf("webhook %s: %d %s", action, rec.Code, rec.Body)
	}
}

func (l *life) activeBypass(pr int) bool {
	_, err := l.st.ActiveBypass(context.Background(), 10, pr)
	return err == nil
}

func (l *life) bypassEndReason(pr int) model.BypassEndReason {
	l.t.Helper()
	list, err := l.st.ListBypasses(context.Background(), store.BypassFilter{RepoID: ptr(int64(10))})
	if err != nil {
		l.t.Fatal(err)
	}
	for _, b := range list {
		if b.PRNumber == pr {
			return b.EndReason
		}
	}
	l.t.Fatalf("no bypass for pr %d", pr)
	return ""
}

func TestBypassLifecycleEndToEnd(t *testing.T) {
	l := newLife(t)
	bob, carol := l.login("bob"), l.login("carol")
	l.gh.setPRs(
		github.PullSummary{Number: 7, HeadSHA: sha(7, 1)},
		github.PullSummary{Number: 8, HeadSHA: sha(8, 1)},
	)
	l.webhook("opened", 7, sha(7, 1), false)
	l.webhook("opened", 8, sha(8, 1), false)
	l.wantCheck(sha(7, 1), "success")

	// Freezing through the API flips both checks via reconcile.
	l.expect(l.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"release"}`), 201)
	l.wantCheck(sha(7, 1), "failure")
	l.wantCheck(sha(8, 1), "failure")

	// A reason is required and only freezers may bypass; nothing changes.
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":" "}`), 400)
	l.expect(l.do(carol, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"x"}`), 403)
	if l.activeBypass(7) {
		t.Fatal("rejected bypass was stored")
	}

	// Creating a bypass flips PR 7 immediately, shows the reason, leaves PR 8 alone.
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"hotfix for outage"}`), 201)
	ck := l.wantCheck(sha(7, 1), "success")
	if !strings.Contains(ck.summary, "hotfix for outage") {
		t.Fatalf("summary lacks reason: %q", ck.summary)
	}
	if l.gh.check(sha(8, 1)).conclusion != "failure" {
		t.Fatal("bypass leaked to PR 8")
	}

	// A new push keeps the bypass.
	l.gh.setPRs(github.PullSummary{Number: 7, HeadSHA: sha(7, 2)}, github.PullSummary{Number: 8, HeadSHA: sha(8, 1)})
	l.webhook("synchronize", 7, sha(7, 2), false)
	l.wantCheck(sha(7, 2), "success")
	if !l.activeBypass(7) {
		t.Fatal("bypass lost on push")
	}

	// Deleting it flips back to failure while frozen.
	l.expect(l.do(bob, "DELETE", "/repos/acme/web/pulls/7/bypass", nil), 204)
	l.wantCheck(sha(7, 2), "failure")

	// Merging clears a bypass.
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"again"}`), 201)
	l.wantCheck(sha(7, 2), "success")
	l.webhook("closed", 7, sha(7, 2), true)
	if l.activeBypass(7) || l.bypassEndReason(7) != model.BypassEndedMerged {
		t.Fatalf("merge did not clear bypass: %v", l.bypassEndReason(7))
	}

	// Closing clears PR 8's bypass too.
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/8/bypass", `{"reason":"abandoned"}`), 201)
	l.webhook("closed", 8, sha(8, 1), false)
	if l.activeBypass(8) || l.bypassEndReason(8) != model.BypassEndedClosed {
		t.Fatal("close did not clear bypass")
	}

	// Thaw clears bypasses: new PR 9 bypassed, then repo thawed.
	l.gh.setPRs(github.PullSummary{Number: 9, HeadSHA: sha(9, 1)})
	l.webhook("opened", 9, sha(9, 1), false)
	l.wantCheck(sha(9, 1), "failure")
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/9/bypass", `{"reason":"ok"}`), 201)
	l.wantCheck(sha(9, 1), "success")
	l.expect(l.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	if l.activeBypass(9) || l.bypassEndReason(9) != model.BypassEndedThawed {
		t.Fatal("thaw did not clear bypass")
	}
}

func TestBypassEndsWhenWindowEnds(t *testing.T) {
	l := newLife(t)
	bob := l.login("bob")
	l.gh.setPRs(github.PullSummary{Number: 7, HeadSHA: sha(7, 1)})
	l.webhook("opened", 7, sha(7, 1), false)
	now := l.clk.Now()
	l.expect(l.do(bob, "POST", "/repos/acme/web/schedules", map[string]any{
		"name": "night", "kind": "one_off", "timezone": "UTC", "starts_at": now.Add(-time.Hour), "ends_at": now.Add(time.Hour), "reason": "deploy window",
	}), 201)
	l.wantCheck(sha(7, 1), "failure")
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"ok"}`), 201)
	l.wantCheck(sha(7, 1), "success")

	// The window ends on its own: no API call, the next evaluation thaws.
	l.clk.advance(2 * time.Hour)
	l.webhook("synchronize", 7, sha(7, 1), false)
	if l.activeBypass(7) || l.bypassEndReason(7) != model.BypassEndedThawed {
		t.Fatal("window end did not clear bypass")
	}
	ended, err := l.st.ListAudit(context.Background(), store.AuditFilter{Action: model.AuditBypassEnd})
	if err != nil || len(ended) != 1 || ended[0].PRNumber == nil || *ended[0].PRNumber != 7 ||
		ended[0].Details != `{"end_reason":"thawed"}` || ended[0].CreatedAt.IsZero() || ended[0].RepoID == nil {
		t.Fatalf("expiry audit = %+v (%v)", ended, err)
	}
}

func (l *life) mergeGroup(pr int, headSHA string) {
	l.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"action":       "checks_requested",
		"installation": map[string]any{"id": 1, "account": map[string]any{"login": "acme", "id": 100, "type": "Organization"}},
		"repository":   map[string]any{"id": 10, "name": "web", "owner": map[string]any{"login": "acme"}, "default_branch": "main"},
		"merge_group":  map[string]any{"head_sha": headSHA, "head_ref": fmt.Sprintf("refs/heads/gh-readonly-queue/main/pr-%d-0123abcd", pr)},
	})
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "merge_group")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	l.svc.ServeHTTP(rec, req)
	if rec.Code != 204 {
		l.t.Fatalf("merge_group: %d %s", rec.Code, rec.Body)
	}
}

func TestMergeGroupRespectsBypass(t *testing.T) {
	l := newLife(t)
	bob := l.login("bob")
	l.expect(l.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"release"}`), 201)
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"urgent"}`), 201)

	l.mergeGroup(7, "mg-7")
	l.mergeGroup(8, "mg-8")
	if ck := l.gh.check("mg-7"); ck.conclusion != "success" || !strings.Contains(ck.summary, "urgent") {
		t.Fatalf("bypassed PR in queue: %+v", ck)
	}
	if ck := l.gh.check("mg-8"); ck.conclusion != "failure" {
		t.Fatalf("other PR in queue: %+v", ck)
	}
	l.expect(l.do(bob, "DELETE", "/repos/acme/web/pulls/7/bypass", nil), 204)
	l.mergeGroup(7, "mg-7b")
	if ck := l.gh.check("mg-7b"); ck.conclusion != "failure" {
		t.Fatalf("revoked bypass in queue: %+v", ck)
	}
}

func TestAuditTrailComplete(t *testing.T) {
	l := newLife(t)
	alice, bob, carol := l.login("alice"), l.login("bob"), l.login("carol")
	now := l.clk.Now()

	l.expect(l.do(alice, "POST", "/orgs/acme/freeze", `{"reason":"quarter end"}`), 201)
	var sc struct{ ID int64 }
	l.expect(l.do(bob, "POST", "/repos/acme/web/schedules", map[string]any{
		"name": "night", "kind": "one_off", "timezone": "UTC", "reason": "deploy window",
		"starts_at": now.Add(time.Hour), "ends_at": now.Add(2 * time.Hour),
	}), 201).decode(&sc)
	l.expect(l.do(bob, "PUT", fmt.Sprintf("/schedules/%d", sc.ID), map[string]any{
		"name": "night2", "kind": "one_off", "timezone": "UTC", "reason": "deploy window",
		"starts_at": now.Add(time.Hour), "ends_at": now.Add(2 * time.Hour), "enabled": true,
	}), 200)
	l.expect(l.do(bob, "DELETE", fmt.Sprintf("/schedules/%d", sc.ID), nil), 204)
	l.expect(l.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"web release"}`), 201)
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/7/bypass", `{"reason":"hotfix"}`), 201)
	l.expect(l.do(carol, "DELETE", "/repos/acme/web/pulls/7/bypass", nil), 403)
	l.expect(l.do(bob, "DELETE", "/repos/acme/web/pulls/7/bypass", nil), 204)
	l.expect(l.do(bob, "POST", "/repos/acme/web/pulls/8/bypass", `{"reason":"second"}`), 201)
	l.expect(l.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	// Org freeze still applies: bypass 8 survives the repo thaw, ends on org thaw.
	l.expect(l.do(alice, "DELETE", "/orgs/acme/freeze", nil), 200)

	var out struct {
		Items      []auditDTO `json:"items"`
		NextBefore int64      `json:"next_before"`
	}
	l.expect(l.do(alice, "GET", "/audit?limit=100", nil), 200).decode(&out)
	type key struct {
		action, scope, actor, reason string
		pr                           int
	}
	got := map[key]auditDTO{}
	for _, it := range out.Items {
		pr := 0
		if it.PRNumber != nil {
			pr = *it.PRNumber
		}
		got[key{it.Action, it.Scope, it.Actor, it.Reason, pr}] = it
		if it.CreatedAt.IsZero() || it.Actor == "" && it.Action != "bypass.end" || it.Org != "acme" {
			t.Errorf("incomplete entry: %+v", it)
		}
	}
	want := []key{
		{"freeze", "org", "alice", "quarter end", 0},
		{"schedule.create", "", "bob", "deploy window", 0},
		{"schedule.update", "", "bob", "deploy window", 0},
		{"schedule.delete", "", "bob", "deploy window", 0},
		{"freeze", "repo", "bob", "web release", 0},
		{"bypass.create", "", "bob", "hotfix", 7},
		{"bypass.end", "", "bob", "", 7},
		{"bypass.create", "", "bob", "second", 8},
		{"thaw", "repo", "bob", "", 0},
		{"thaw", "org", "alice", "", 0},
		{"bypass.end", "", "alice", "", 8},
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing audit entry %+v", w)
		}
	}
	if len(out.Items) != len(want) {
		t.Errorf("got %d entries, want %d", len(out.Items), len(want))
	}
	if d := got[key{"bypass.end", "", "bob", "", 7}].Details["end_reason"]; d != "revoked" {
		t.Errorf("revoke details = %v", d)
	}
	if d := got[key{"bypass.end", "", "alice", "", 8}].Details["end_reason"]; d != "thawed" {
		t.Errorf("thaw details = %v", d)
	}
	if got[key{"freeze", "repo", "bob", "web release", 0}].Repo != "acme/web" {
		t.Errorf("repo scope missing")
	}

	// Filters.
	count := func(q string) int {
		var o struct{ Items []auditDTO }
		l.expect(l.do(alice, "GET", "/audit?"+q, nil), 200).decode(&o)
		return len(o.Items)
	}
	if n := count("action=bypass.create"); n != 2 {
		t.Errorf("action filter: %d", n)
	}
	if n := count("actor=alice"); n != 3 {
		t.Errorf("actor filter: %d", n)
	}
	if n := count("repo=acme/web&action=thaw"); n != 1 {
		t.Errorf("repo filter: %d", n)
	}
	if n := count("org=acme"); n != len(want) {
		t.Errorf("org filter: %d", n)
	}

	// Pagination walks newest to oldest without gaps or repeats.
	seen := map[int64]bool{}
	before := ""
	for pages := 0; pages < 10; pages++ {
		var o struct {
			Items      []auditDTO `json:"items"`
			NextBefore int64      `json:"next_before"`
		}
		l.expect(l.do(alice, "GET", "/audit?limit=4"+before, nil), 200).decode(&o)
		for _, it := range o.Items {
			if seen[it.ID] {
				t.Fatalf("entry %d repeated", it.ID)
			}
			seen[it.ID] = true
		}
		if o.NextBefore == 0 {
			break
		}
		before = fmt.Sprintf("&before=%d", o.NextBefore)
	}
	if len(seen) != len(want) {
		t.Errorf("paged %d entries, want %d", len(seen), len(want))
	}
}
