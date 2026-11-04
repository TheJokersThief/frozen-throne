//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// RepoRef names one repository of the env's installation.
type RepoRef struct {
	Name string
	ID   int64
}

// Primary is the repository every Env starts with.
func (e *Env) Primary() RepoRef { return RepoRef{Name: e.Repo, ID: e.RepoID} }

// NewRepo creates a repository in the mock and announces it to the app the way
// GitHub does when it is added to the installation
// (installation_repositories.added).
func (e *Env) NewRepo(name string) RepoRef {
	e.T.Helper()
	r := e.AddRepoRef(name)
	e.Deliver(webhooks.InstallationRepositoriesAdded(e.RepoParams(r, 1, e.SHA("add-"+name))))
	return r
}

// AddRepoRef creates a repository in the mock only: no webhook, so the app
// does not know about it yet.
func (e *Env) AddRepoRef(name string) RepoRef {
	e.T.Helper()
	return RepoRef{Name: name, ID: e.AddRepo(name)}
}

// RepoParams is Params for repository r.
func (e *Env) RepoParams(r RepoRef, pr int, sha string) webhooks.Params {
	p := e.Params(pr, sha)
	p.Repo, p.RepoID = r.Name, r.ID
	return p
}

// OpenPRIn seeds PR number/sha in r in the mock and delivers
// pull_request.opened.
func (e *Env) OpenPRIn(r RepoRef, number int, sha string) {
	e.T.Helper()
	e.SeedPRIn(r, number, sha)
	e.Deliver(webhooks.PullRequestOpened(e.RepoParams(r, number, sha)))
}

// SeedPRIn registers an open PR in r in the mock without any webhook.
func (e *Env) SeedPRIn(r RepoRef, number int, sha string) {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.S.Mock.SeedPR(ctx, mockgithub.PR{Owner: e.Org, Repo: r.Name, Number: number, HeadSHA: sha}); err != nil {
		e.T.Fatalf("seed PR: %v", err)
	}
}

// ChecksIn lists the frozen-throne check runs for sha in r.
func (e *Env) ChecksIn(r RepoRef, sha string) []mockgithub.CheckRun {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runs, err := e.S.Mock.CheckRuns(ctx, e.Org, r.Name, sha, CheckName)
	if err != nil {
		e.T.Fatalf("list check runs: %v", err)
	}
	return runs
}

// Verdict is the newest frozen-throne check on sha in r: "none" when there is
// no check, "pending" while it has not completed, else its conclusion.
func (e *Env) Verdict(r RepoRef, sha string) string {
	e.T.Helper()
	runs := e.ChecksIn(r, sha)
	if len(runs) == 0 {
		return "none"
	}
	last := runs[len(runs)-1]
	if last.Status != "completed" {
		return "pending"
	}
	return last.Conclusion
}

// WaitVerdict polls until the newest check on sha in r has this verdict. On
// timeout it fails with the recorded GitHub calls (see Dump).
func (e *Env) WaitVerdict(r RepoRef, sha, want string) {
	e.T.Helper()
	e.Eventually(fmt.Sprintf("check on %s/%s@%.7s to be %q", e.Org, r.Name, sha, want), func() bool {
		return e.Verdict(r, sha) == want
	})
}

// Eventually polls cond every 25ms until it holds, failing the test with
// diagnostics after 20 seconds. It replaces sleeping: nothing here waits
// longer than the condition needs.
func (e *Env) Eventually(what string, cond func() bool) {
	e.T.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.T.Fatalf("timed out waiting for %s\n%s", what, e.Dump())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Dump renders what the mock saw: every check run and the mutating or
// check-related calls the app made, in order. It is appended to failures.
func (e *Env) Dump() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	runs, err := e.S.Mock.CheckRuns(ctx, "", "", "", "")
	fmt.Fprintf(&b, "--- check runs in mock (err=%v) ---\n", err)
	for _, r := range runs {
		if r.Owner != e.Org {
			continue
		}
		fmt.Fprintf(&b, "  #%d %s/%s@%.7s %s %s/%s %q\n", r.ID, r.Owner, r.Repo, r.HeadSHA, r.Name, r.Status, r.Conclusion, r.Output.Title)
	}
	calls, err := e.S.Mock.Calls(ctx)
	fmt.Fprintf(&b, "--- GitHub calls recorded by mock (err=%v) ---\n", err)
	for _, c := range calls {
		// The app's database outlives each test's mock reset, so its periodic
		// pass also re-checks earlier tests' repos; show only this env's calls.
		mine := strings.Contains(c.Path, "/"+e.Org+"/") || strings.Contains(c.Path, fmt.Sprint("/installations/", e.InstallationID, "/"))
		if !mine || (c.Method == http.MethodGet && !strings.Contains(c.Path, "/pulls")) {
			continue
		}
		fmt.Fprintf(&b, "  #%d %s %s -> %d %s\n", c.Seq, c.Method, c.Path, c.Status, truncate(c.Body, 200))
	}
	posts, err := e.S.Mock.SlackPosts(ctx)
	fmt.Fprintf(&b, "--- fake Slack posts (err=%v) ---\n", err)
	for _, p := range posts {
		fmt.Fprintf(&b, "  %s: %s\n", p.Hook, truncate(p.Body, 300))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ---- API helpers ----

func (e *Env) repoPath(r RepoRef) string { return "/api/v1/repos/" + e.Org + "/" + r.Name }

// expect performs the call and requires status want; want 2 means any 2xx.
func (e *Env) expect(sess *Session, want int, method, path string, body any) []byte {
	e.T.Helper()
	st, data := sess.Do(method, path, body)
	if st != want && !(want == 2 && st/100 == 2) {
		e.T.Fatalf("%s %s as %s: status %d, want %d: %s\n%s", method, path, sess.Login, st, want, data, e.Dump())
	}
	return data
}

// FreezeRepo freezes r through the API (must succeed).
func (e *Env) FreezeRepo(sess *Session, r RepoRef, reason string) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodPost, e.repoPath(r)+"/freeze", map[string]string{"reason": reason})
}

// ThawRepo thaws r through the API (must succeed).
func (e *Env) ThawRepo(sess *Session, r RepoRef) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodDelete, e.repoPath(r)+"/freeze", nil)
}

// FreezeOrg freezes the whole org through the API (must succeed).
func (e *Env) FreezeOrg(sess *Session, reason string) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodPost, "/api/v1/orgs/"+e.Org+"/freeze", map[string]string{"reason": reason})
}

// ThawOrg lifts the org-wide freeze (must succeed).
func (e *Env) ThawOrg(sess *Session) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodDelete, "/api/v1/orgs/"+e.Org+"/freeze", nil)
}

// Bypass bypasses PR number in r (must succeed).
func (e *Env) Bypass(sess *Session, r RepoRef, number int, reason string) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodPost, fmt.Sprintf("%s/pulls/%d/bypass", e.repoPath(r), number), map[string]string{"reason": reason})
}

// AuditEntry is the subset of an audit log row the scenarios assert on.
type AuditEntry struct {
	Action   string         `json:"action"`
	Org      string         `json:"org"`
	Repo     string         `json:"repo"`
	Scope    string         `json:"scope"`
	Actor    string         `json:"actor"`
	PRNumber *int           `json:"pr_number"`
	Reason   string         `json:"reason"`
	Details  map[string]any `json:"details"`
}

// Audit returns the org's audit log, newest first.
func (e *Env) Audit(sess *Session) []AuditEntry {
	e.T.Helper()
	var out struct {
		Items []AuditEntry `json:"items"`
	}
	st := sess.JSON(http.MethodGet, "/api/v1/audit?limit=200&org="+e.Org, nil, &out)
	if st != http.StatusOK {
		e.T.Fatalf("audit: status %d", st)
	}
	return out.Items
}

// ConfigureSlack points the org's Slack webhook at the fake Slack in the mock
// and subscribes to events. sess must be an org owner.
func (e *Env) ConfigureSlack(sess *Session, hook string, events ...string) {
	e.T.Helper()
	e.expect(sess, 2, http.MethodPut, "/api/v1/orgs/"+e.Org+"/slack", map[string]any{
		"webhook_url": e.S.MockInternalURL + "/_mock/slack/hooks/" + hook,
		"enabled":     true,
		"events":      events,
	})
}

// SlackPosts returns what the fake Slack received on hook.
func (e *Env) SlackPosts(hook string) []string {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all, err := e.S.Mock.SlackPosts(ctx)
	if err != nil {
		e.T.Fatalf("slack posts: %v", err)
	}
	var out []string
	for _, p := range all {
		if p.Hook == hook {
			out = append(out, p.Body)
		}
	}
	return out
}
