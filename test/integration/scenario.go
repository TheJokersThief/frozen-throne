//go:build integration

package integration

import (
	"context"
	"crypto/sha1" //nolint:gosec // fake commit ids only
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// CheckName is the Check Run the app posts.
const CheckName = "frozen-throne"

// Env is one test's isolated world: an installation on a fresh org with one
// repository and three users, all seeded in the mock. Every id and name is
// unique across tests (and across runs against a long-lived external stack),
// because the app keeps its own database between tests.
type Env struct {
	T *testing.T
	S *Stack

	InstallationID int64
	AccountID      int64
	RepoID         int64
	Org            string // also the repo owner
	Repo           string

	Owner      string // org owner (admin role): may also freeze the whole org and manage Slack
	Maintainer string // may freeze: maintain on the repo, org member
	Member     string // read-only: pull on the repo, org member
	Outsider   string // no access at all

	maintainer mockgithub.User
}

func newEnv(t *testing.T, s *Stack, n int) *Env {
	t.Helper()
	// Seconds since the epoch keep ids unique across runs; n across tests.
	base := (time.Now().Unix()%100_000_000)*1000 + int64(n)
	e := &Env{
		T: t, S: s,
		InstallationID: base, AccountID: base + 1_000_000_000_000, RepoID: base + 2_000_000_000_000,
		Org: fmt.Sprintf("org%d", base), Repo: "widgets",
		Owner: fmt.Sprintf("owner%d", base), Maintainer: fmt.Sprintf("maint%d", base), Member: fmt.Sprintf("member%d", base), Outsider: fmt.Sprintf("outsider%d", base),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(s.Mock.SeedInstallation(ctx, mockgithub.Installation{ID: e.InstallationID, AccountLogin: e.Org, AccountID: e.AccountID, AccountType: "Organization"}))
	must(s.Mock.SeedRepo(ctx, mockgithub.Repo{InstallationID: e.InstallationID, ID: e.RepoID, Owner: e.Org, Name: e.Repo, DefaultBranch: "main"}))
	full := e.Org + "/" + e.Repo
	users := []mockgithub.User{
		{ID: base + 3_000_000_000_000, Login: e.Maintainer, Name: "Maintainer", RepoPerms: map[string]string{full: "maintain"}, OrgRoles: map[string]string{e.Org: "member"}},
		{ID: base + 4_000_000_000_000, Login: e.Member, Name: "Member", RepoPerms: map[string]string{full: "pull"}, OrgRoles: map[string]string{e.Org: "member"}},
		{ID: base + 5_000_000_000_000, Login: e.Outsider, Name: "Outsider"},
		{ID: base + 6_000_000_000_000, Login: e.Owner, Name: "Owner", OrgRoles: map[string]string{e.Org: "admin"}},
	}
	for _, u := range users {
		_, err := s.Mock.SeedUser(ctx, u)
		must(err)
	}
	e.maintainer = users[0]
	return e
}

// AddRepo creates another repository in the env's installation in the mock and
// gives Maintainer maintain rights on it. It sends no webhook. Returns the
// repo id.
func (e *Env) AddRepo(name string) int64 {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A separate id range with a stride wider than the per-env step (1), so
	// neither RepoID+n nor another env's added repos can coincide.
	id := e.RepoID + 500_000_000_000 + int64(len(e.maintainer.RepoPerms))*1_000_000
	if err := e.S.Mock.SeedRepo(ctx, mockgithub.Repo{InstallationID: e.InstallationID, ID: id, Owner: e.Org, Name: name, DefaultBranch: "main"}); err != nil {
		e.T.Fatalf("seed repo: %v", err)
	}
	e.maintainer.RepoPerms[e.Org+"/"+name] = "maintain"
	if _, err := e.S.Mock.SeedUser(ctx, e.maintainer); err != nil {
		e.T.Fatalf("grant repo: %v", err)
	}
	return id
}

// ListRepos returns the full names the user sees in GET /api/v1/repos.
func (e *Env) ListRepos(sess *Session) []string {
	e.T.Helper()
	var out struct {
		Items []struct {
			FullName string `json:"full_name"`
		} `json:"items"`
	}
	if st := sess.JSON(http.MethodGet, "/api/v1/repos?org="+e.Org, nil, &out); st != http.StatusOK {
		e.T.Fatalf("list repos: status %d", st)
	}
	names := []string{}
	for _, it := range out.Items {
		names = append(names, it.FullName)
	}
	return names
}

// SHA returns a stable, unique-per-test 40-hex commit id for label.
func (e *Env) SHA(label string, parts ...any) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%d/%s/%v", e.RepoID, label, parts))) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// Params returns webhook fixture parameters for this env's repo.
func (e *Env) Params(pr int, sha string) webhooks.Params {
	return webhooks.Params{
		InstallationID: e.InstallationID, AccountLogin: e.Org, AccountID: e.AccountID, AccountType: "Organization",
		Owner: e.Org, Repo: e.Repo, RepoID: e.RepoID, PRNumber: pr, HeadSHA: sha, Sender: e.Maintainer,
	}
}

// Deliver sends d with the stack's valid signature and requires a 2xx answer.
func (e *Env) Deliver(d webhooks.Delivery) webhooks.Response {
	e.T.Helper()
	r := e.TryDeliver(e.S.Webhooks, d)
	if r.Status/100 != 2 {
		e.T.Fatalf("%s delivery: status %d: %s", d.Event, r.Status, r.Body)
	}
	return r
}

// TryDeliver sends d through snd and returns the response whatever its status.
func (e *Env) TryDeliver(snd *webhooks.Sender, d webhooks.Delivery) webhooks.Response {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := snd.Send(ctx, d)
	if err != nil {
		e.T.Fatalf("send %s: %v", d.Event, err)
	}
	return r
}

// Install delivers installation.created for this env. Repo-level events must
// follow it: see TestPullRequestBeforeInstallation.
func (e *Env) Install() {
	e.T.Helper()
	e.Deliver(webhooks.InstallationCreated(e.Params(1, e.SHA("install"))))
}

// OpenPR registers PR number/sha in the mock (as GitHub would have it) and
// delivers pull_request.opened.
func (e *Env) OpenPR(number int, sha string) {
	e.T.Helper()
	e.SeedPR(number, sha)
	e.Deliver(webhooks.PullRequestOpened(e.Params(number, sha)))
}

// SeedPR registers an open PR in the mock without any webhook.
func (e *Env) SeedPR(number int, sha string) {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.S.Mock.SeedPR(ctx, mockgithub.PR{Owner: e.Org, Repo: e.Repo, Number: number, HeadSHA: sha}); err != nil {
		e.T.Fatalf("seed PR: %v", err)
	}
}

// Checks lists the frozen-throne check runs the mock holds for sha.
func (e *Env) Checks(sha string) []mockgithub.CheckRun {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runs, err := e.S.Mock.CheckRuns(ctx, e.Org, e.Repo, sha, CheckName)
	if err != nil {
		e.T.Fatalf("list check runs: %v", err)
	}
	return runs
}

// AllChecks lists every check run in this env's repo regardless of sha.
func (e *Env) AllChecks() []mockgithub.CheckRun {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runs, err := e.S.Mock.CheckRuns(ctx, e.Org, e.Repo, "", "")
	if err != nil {
		e.T.Fatalf("list check runs: %v", err)
	}
	return runs
}

// WaitCheck waits for a frozen-throne check run on sha satisfying pred (nil
// accepts any) and returns it. Webhook handling is synchronous in the app, so
// this only absorbs transport latency.
func (e *Env) WaitCheck(sha string, pred func(mockgithub.CheckRun) bool) mockgithub.CheckRun {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cr, err := e.S.Mock.WaitForCheckRun(ctx, e.Org, e.Repo, sha, func(c mockgithub.CheckRun) bool {
		return c.Name == CheckName && (pred == nil || pred(c))
	})
	if err != nil {
		e.T.Fatalf("%v (have %+v)", err, e.Checks(sha))
	}
	return cr
}

// Conclusion is a predicate matching a completed run with this conclusion.
func Conclusion(c string) func(mockgithub.CheckRun) bool {
	return func(r mockgithub.CheckRun) bool { return r.Status == "completed" && r.Conclusion == c }
}

// Freeze freezes the env's repo through the real API as sess, which must be
// logged in as a user with maintain/admin on it. It requires a 2xx answer.
func (e *Env) Freeze(sess *Session, reason string) {
	e.T.Helper()
	st, body := sess.Do(http.MethodPost, "/api/v1/repos/"+e.Org+"/"+e.Repo+"/freeze", map[string]string{"reason": reason})
	if st/100 != 2 {
		e.T.Fatalf("freeze: status %d: %s", st, body)
	}
}
