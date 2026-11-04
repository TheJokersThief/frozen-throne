//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// activeBypassPRs lists the PR numbers with an active bypass in r.
func (e *Env) activeBypassPRs(sess *Session, r RepoRef) []int {
	e.T.Helper()
	var out struct {
		Items []struct {
			PRNumber int `json:"pr_number"`
		} `json:"items"`
	}
	if st := sess.JSON(http.MethodGet, e.repoPath(r)+"/bypasses?active=true", nil, &out); st != http.StatusOK {
		e.T.Fatalf("list bypasses: status %d", st)
	}
	var prs []int
	for _, b := range out.Items {
		prs = append(prs, b.PRNumber)
	}
	return prs
}

// requireBypassed fails unless the active bypasses are exactly want.
func (e *Env) requireBypassed(sess *Session, r RepoRef, want ...int) {
	e.T.Helper()
	got := e.activeBypassPRs(sess, r)
	if len(got) != len(want) {
		e.T.Fatalf("active bypasses = %v, want %v\n%s", got, want, e.Dump())
	}
	for i := range want {
		if got[i] != want[i] {
			e.T.Fatalf("active bypasses = %v, want %v\n%s", got, want, e.Dump())
		}
	}
}

// lastBypassEnd returns the end_reason of the newest bypass.end audit entry for pr.
func (e *Env) lastBypassEnd(sess *Session, pr int) string {
	e.T.Helper()
	for _, a := range e.Audit(sess) {
		if a.Action == "bypass.end" && a.PRNumber != nil && *a.PRNumber == pr {
			s, _ := a.Details["end_reason"].(string)
			return s
		}
	}
	return ""
}

// Scenario 4: a per-PR bypass passes only that PR, survives a push to it, and
// ends when the PR closes, merges or the freeze ends.
func TestScenarioBypassLifecycle(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	maint := stack.Login(t, e.Maintainer)

	sha := map[int]string{}
	for n := 1; n <= 4; n++ {
		sha[n] = e.SHA("pr", n)
		e.OpenPR(n, sha[n])
	}
	e.FreezeRepo(maint, repo, "release freeze")
	for n := 1; n <= 4; n++ {
		e.WaitVerdict(repo, sha[n], "failure")
	}

	// Only the bypassed PR passes, and its check says why.
	e.Bypass(maint, repo, 1, "hotfix for incident 42")
	e.WaitVerdict(repo, sha[1], "success")
	if r := e.ChecksIn(repo, sha[1]); !strings.Contains(r[len(r)-1].Output.Summary, "hotfix for incident 42") {
		t.Fatalf("bypassed check does not carry the reason: %+v", r[len(r)-1])
	}
	for n := 2; n <= 4; n++ {
		if v := e.Verdict(repo, sha[n]); v != "failure" {
			t.Fatalf("PR %d verdict = %s, want failure\n%s", n, v, e.Dump())
		}
	}

	// A push to the bypassed PR is a new head SHA; the bypass is per PR so it still passes.
	pushed := e.SHA("pr", 1, "pushed")
	e.SeedPR(1, pushed)
	e.Deliver(webhooks.PullRequestSynchronize(e.Params(1, pushed)))
	e.WaitVerdict(repo, pushed, "success")
	e.requireBypassed(maint, repo, 1)

	// Closing the PR ends its bypass.
	e.Deliver(webhooks.PullRequestClosed(e.Params(1, pushed)))
	e.requireBypassed(maint, repo)
	if got := e.lastBypassEnd(maint, 1); got != "closed" {
		t.Fatalf("PR 1 bypass end_reason = %q, want closed", got)
	}
	// Reopened while still frozen: red again, the old bypass does not come back.
	e.Deliver(webhooks.PullRequestReopened(e.Params(1, pushed)))
	e.WaitVerdict(repo, pushed, "failure")

	// Merging ends it too.
	e.Bypass(maint, repo, 2, "release blocker")
	e.WaitVerdict(repo, sha[2], "success")
	e.Deliver(webhooks.PullRequestMerged(e.Params(2, sha[2])))
	e.requireBypassed(maint, repo)
	if got := e.lastBypassEnd(maint, 2); got != "merged" {
		t.Fatalf("PR 2 bypass end_reason = %q, want merged", got)
	}

	// Ending the freeze ends remaining bypasses, so a later freeze starts clean.
	e.Bypass(maint, repo, 3, "temporary exception")
	e.WaitVerdict(repo, sha[3], "success")
	e.ThawRepo(maint, repo)
	e.requireBypassed(maint, repo)
	if got := e.lastBypassEnd(maint, 3); got != "thawed" {
		t.Fatalf("PR 3 bypass end_reason = %q, want thawed", got)
	}
	e.WaitVerdict(repo, sha[3], "success")
	e.FreezeRepo(maint, repo, "second freeze")
	e.WaitVerdict(repo, sha[3], "failure")
	e.WaitVerdict(repo, sha[4], "failure")
}

// Scenario 4b: revoking a bypass puts the PR back behind the freeze.
func TestScenarioBypassRevoked(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	maint := stack.Login(t, e.Maintainer)
	sha := e.SHA("head")
	e.OpenPR(1, sha)
	e.FreezeRepo(maint, repo, "freeze")
	e.Bypass(maint, repo, 1, "exception")
	e.WaitVerdict(repo, sha, "success")

	e.expect(maint, 2, http.MethodDelete, e.repoPath(repo)+"/pulls/1/bypass", nil)
	e.WaitVerdict(repo, sha, "failure")
	if got := e.lastBypassEnd(maint, 1); got != "revoked" {
		t.Fatalf("end_reason = %q, want revoked", got)
	}
}

// Scenario 5: a PR whose webhook never arrived gets its check once the
// reconciler runs (it re-lists open PRs from GitHub on a timer).
func TestScenarioMissedWebhookHeals(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	maint := stack.Login(t, e.Maintainer)

	// Open PR the app never heard about: seeded in the mock, no event.
	missed := e.SHA("missed")
	e.SeedPR(5, missed)
	e.WaitVerdict(repo, missed, "success")

	// Same while frozen: the healed check is correct for the current state.
	e.FreezeRepo(maint, repo, "freeze")
	missedFrozen := e.SHA("missed-frozen")
	e.SeedPR(6, missedFrozen)
	e.WaitVerdict(repo, missedFrozen, "failure")
	e.WaitVerdict(repo, missed, "failure")
}
