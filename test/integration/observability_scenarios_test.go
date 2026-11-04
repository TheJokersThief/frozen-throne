//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// requireAudit fails unless the log holds an entry with this action by actor
// for repo (empty = org-level) and, when pr is non-zero, that PR.
func (e *Env) requireAudit(log []AuditEntry, action, actor, repo string, pr int) AuditEntry {
	e.T.Helper()
	for _, a := range log {
		if a.Action != action || a.Actor != actor || a.Repo != repo || a.Org != e.Org {
			continue
		}
		if pr != 0 && (a.PRNumber == nil || *a.PRNumber != pr) {
			continue
		}
		return a
	}
	e.T.Fatalf("no audit entry action=%s actor=%s repo=%q pr=%d in %+v", action, actor, repo, pr, log)
	return AuditEntry{}
}

// Scenario 7: every state-changing action leaves an audit entry naming the
// actor, target and reason.
func TestScenarioAuditLogRecordsEveryAction(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	full := e.Org + "/" + repo.Name
	maint := stack.Login(t, e.Maintainer)
	owner := stack.Login(t, e.Owner)
	sha := e.SHA("head")
	e.OpenPR(1, sha)

	e.FreezeRepo(maint, repo, "repo freeze reason")
	e.Bypass(maint, repo, 1, "bypass reason")
	e.expect(maint, 2, http.MethodDelete, e.repoPath(repo)+"/pulls/1/bypass", nil)
	e.ThawRepo(maint, repo)
	e.FreezeOrg(owner, "org freeze reason")
	e.ThawOrg(owner)

	var created struct {
		ID int64 `json:"id"`
	}
	start := time.Now().Add(48 * time.Hour).UTC()
	window := map[string]any{
		"name": "audit window", "reason": "window reason", "kind": "one_off", "timezone": "UTC",
		"starts_at": start.Format(time.RFC3339), "ends_at": start.Add(time.Hour).Format(time.RFC3339),
	}
	if st := maint.JSON(http.MethodPost, e.repoPath(repo)+"/schedules", window, &created); st/100 != 2 {
		t.Fatalf("create schedule: %d", st)
	}
	window["name"] = "audit window v2"
	e.expect(maint, 2, http.MethodPut, fmt.Sprintf("/api/v1/schedules/%d", created.ID), window)
	e.expect(maint, 2, http.MethodDelete, fmt.Sprintf("/api/v1/schedules/%d", created.ID), nil)

	e.expect(maint, 2, http.MethodPut, e.repoPath(repo)+"/public-status", map[string]bool{"enabled": true})
	e.ConfigureSlack(owner, "audit", "freeze")

	log := e.Audit(maint)
	if got := e.requireAudit(log, "freeze", e.Maintainer, full, 0); got.Reason != "repo freeze reason" || got.Scope != "repo" {
		t.Errorf("repo freeze entry = %+v", got)
	}
	e.requireAudit(log, "thaw", e.Maintainer, full, 0)
	if got := e.requireAudit(log, "bypass.create", e.Maintainer, full, 1); got.Reason != "bypass reason" {
		t.Errorf("bypass entry = %+v", got)
	}
	if got := e.requireAudit(log, "bypass.end", e.Maintainer, full, 1); got.Details["end_reason"] != "revoked" {
		t.Errorf("bypass end entry = %+v", got)
	}
	if got := e.requireAudit(log, "freeze", e.Owner, "", 0); got.Reason != "org freeze reason" || got.Scope != "org" {
		t.Errorf("org freeze entry = %+v", got)
	}
	e.requireAudit(log, "thaw", e.Owner, "", 0)
	e.requireAudit(log, "schedule.create", e.Maintainer, full, 0)
	e.requireAudit(log, "schedule.update", e.Maintainer, full, 0)
	e.requireAudit(log, "schedule.delete", e.Maintainer, full, 0)
	e.requireAudit(log, "public_status.update", e.Maintainer, full, 0)
	e.requireAudit(log, "slack.update", e.Owner, "", 0)

	// The webhook URL is a secret: it must not appear anywhere in the log.
	for _, a := range log {
		if strings.Contains(fmt.Sprint(a), "_mock/slack") {
			t.Errorf("audit entry leaks the Slack webhook URL: %+v", a)
		}
	}
}

// publicGet fetches a public (unauthenticated) endpoint.
func (e *Env) publicGet(path string) (int, string, string) {
	e.T.Helper()
	resp, err := http.Get(e.S.AppURL + path)
	if err != nil {
		e.T.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// Scenario 8: the public status and badge exist only for repos that opted in,
// and say nothing beyond frozen / since / until.
func TestScenarioPublicStatusOnlyForOptedInRepos(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	public := e.Primary()
	private := e.NewRepo("private-repo")
	maint := stack.Login(t, e.Maintainer)
	owner := stack.Login(t, e.Owner)
	statusOf := func(r RepoRef) string { return "/api/v1/public/repos/" + e.Org + "/" + r.Name + "/status" }
	badgeOf := func(r RepoRef) string { return "/api/v1/public/repos/" + e.Org + "/" + r.Name + "/badge.svg" }
	const secretReason = "board-approved acquisition of ExampleCorp"

	e.FreezeOrg(owner, secretReason) // frozen, but nobody opted in yet
	sha := e.SHA("head")
	e.OpenPR(1, sha)

	// Opted-out, unknown and removed-looking repos are indistinguishable.
	for _, path := range []string{
		statusOf(public), statusOf(private), statusOf(RepoRef{Name: "does-not-exist"}),
		badgeOf(public), badgeOf(RepoRef{Name: "does-not-exist"}),
	} {
		code, _, body := e.publicGet(path)
		if code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404 for a repo that has not opted in: %s", path, code, body)
		}
		if strings.Contains(body, secretReason) || strings.Contains(body, e.Maintainer) || strings.Contains(body, e.Owner) {
			t.Fatalf("404 for %s leaks details: %s", path, body)
		}
	}

	e.expect(maint, 2, http.MethodPut, e.repoPath(public)+"/public-status", map[string]bool{"enabled": true})

	code, ctype, body := e.publicGet(statusOf(public))
	if code != http.StatusOK || !strings.Contains(ctype, "json") {
		t.Fatalf("opted-in status = %d %s: %s", code, ctype, body)
	}
	if !strings.Contains(body, `"frozen":true`) {
		t.Fatalf("opted-in repo under an org freeze should report frozen: %s", body)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("public status is not JSON: %v: %s", err, body)
	}
	for k := range fields {
		if k != "repo" && k != "frozen" && k != "since" && k != "until" {
			t.Fatalf("public status exposes field %q: %s", k, body)
		}
	}
	for _, secret := range []string{secretReason, e.Owner, e.Maintainer, sha} {
		if strings.Contains(body, secret) {
			t.Fatalf("public status leaks %q: %s", secret, body)
		}
	}
	code, ctype, badge := e.publicGet(badgeOf(public))
	if code != http.StatusOK || !strings.Contains(ctype, "svg") || !strings.Contains(badge, "frozen") || strings.Contains(badge, secretReason) {
		t.Fatalf("badge = %d %s: %s", code, ctype, badge)
	}

	// The sibling repo is under the same org freeze but still invisible.
	if code, _, body := e.publicGet(statusOf(private)); code != http.StatusNotFound {
		t.Fatalf("repo that did not opt in is visible: %d %s", code, body)
	}

	e.ThawOrg(owner)
	if _, _, body := e.publicGet(statusOf(public)); !strings.Contains(body, `"frozen":false`) {
		t.Fatalf("after thaw: %s", body)
	}
	if _, _, badge := e.publicGet(badgeOf(public)); !strings.Contains(badge, "open") {
		t.Fatalf("badge after thaw: %s", badge)
	}

	// Opting out hides it again.
	e.expect(maint, 2, http.MethodPut, e.repoPath(public)+"/public-status", map[string]bool{"enabled": false})
	if code, _, _ := e.publicGet(statusOf(public)); code != http.StatusNotFound {
		t.Fatalf("opted-out repo still public: %d", code)
	}
}

// waitSlack waits until the fake Slack has received n posts on hook.
func (e *Env) waitSlack(hook string, n int) []string {
	e.T.Helper()
	var posts []string
	e.Eventually(fmt.Sprintf("%d Slack post(s) on %q", n, hook), func() bool {
		posts = e.SlackPosts(hook)
		return len(posts) >= n
	})
	return posts
}

// Scenario 9: freeze and thaw are announced to the org's Slack webhook.
func TestScenarioSlackNotifiedOnFreezeAndThaw(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	full := e.Org + "/" + repo.Name
	maint := stack.Login(t, e.Maintainer)
	owner := stack.Login(t, e.Owner)
	hook := fmt.Sprintf("hook%d", e.InstallationID)
	e.ConfigureSlack(owner, hook, "freeze", "thaw")

	e.FreezeRepo(maint, repo, "slack freeze reason")
	posts := e.waitSlack(hook, 1)
	for _, want := range []string{full, e.Maintainer, "slack freeze reason"} {
		if !strings.Contains(posts[0], want) {
			t.Errorf("freeze message lacks %q: %s", want, posts[0])
		}
	}

	e.ThawRepo(maint, repo)
	posts = e.waitSlack(hook, 2)
	if !strings.Contains(posts[1], full) || !strings.Contains(posts[1], e.Maintainer) {
		t.Errorf("thaw message = %s", posts[1])
	}
	if posts[0] == posts[1] {
		t.Errorf("freeze and thaw produced identical messages: %s", posts[0])
	}
}
