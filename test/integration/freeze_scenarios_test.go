//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// Scenario 1: freezing a repo through the API flips the check of every open PR
// to failure; thawing flips them all back.
func TestScenarioRepoFreezeFlipsEveryOpenPR(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	shas := map[int]string{}
	for _, n := range []int{1, 2, 3} {
		shas[n] = e.SHA("pr", n)
		e.OpenPR(n, shas[n])
	}
	for n, sha := range shas {
		e.WaitVerdict(repo, sha, "success")
		_ = n
	}

	maint := stack.Login(t, e.Maintainer)
	e.FreezeRepo(maint, repo, "release cut")
	for _, sha := range shas {
		e.WaitVerdict(repo, sha, "failure")
	}

	e.ThawRepo(maint, repo)
	for _, sha := range shas {
		e.WaitVerdict(repo, sha, "success")
	}
}

// Scenario 2: an org-wide freeze covers repos that already exist and a repo
// added to the installation afterwards; thawing the org clears all of them.
func TestScenarioOrgFreezeCoversExistingAndLaterRepos(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	first := e.Primary()
	second := e.NewRepo("gadgets")
	firstSHA, secondSHA := e.SHA("first"), e.SHA("second")
	e.OpenPRIn(first, 1, firstSHA)
	e.OpenPRIn(second, 1, secondSHA)
	e.WaitVerdict(first, firstSHA, "success")
	e.WaitVerdict(second, secondSHA, "success")

	owner := stack.Login(t, e.Owner)
	e.FreezeOrg(owner, "company-wide code freeze")
	e.WaitVerdict(first, firstSHA, "failure")
	e.WaitVerdict(second, secondSHA, "failure")

	// A repo that joins the installation while the org is frozen starts frozen.
	late := e.NewRepo("late-comer")
	lateSHA := e.SHA("late")
	e.OpenPRIn(late, 1, lateSHA)
	e.WaitVerdict(late, lateSHA, "failure")

	e.ThawOrg(owner)
	e.WaitVerdict(first, firstSHA, "success")
	e.WaitVerdict(second, secondSHA, "success")
	e.WaitVerdict(late, lateSHA, "success")
}

// Scenario 6: a user without admin rights gets 403 and nothing changes.
func TestScenarioPermissionDenied(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	sha := e.SHA("head")
	e.OpenPR(1, sha)
	e.WaitVerdict(repo, sha, "success")
	runsBefore := len(e.ChecksIn(repo, sha))

	member := stack.Login(t, e.Member) // read-only on the repo
	e.expect(member, http.StatusForbidden, http.MethodPost, e.repoPath(repo)+"/freeze", map[string]string{"reason": "nope"})
	e.expect(member, http.StatusForbidden, http.MethodPost, "/api/v1/orgs/"+e.Org+"/freeze", map[string]string{"reason": "nope"})
	e.expect(member, http.StatusForbidden, http.MethodPost, e.repoPath(repo)+"/pulls/1/bypass", map[string]string{"reason": "nope"})

	// Freeze state is committed synchronously, so reading it back is a
	// deterministic proof that nothing was applied (no sleeping needed).
	maint := stack.Login(t, e.Maintainer)
	var got struct {
		Frozen bool `json:"frozen"`
	}
	if st := maint.JSON(http.MethodGet, e.repoPath(repo), nil, &got); st != http.StatusOK || got.Frozen {
		t.Fatalf("repo after denied freeze: status %d frozen=%v\n%s", st, got.Frozen, e.Dump())
	}
	if v := e.Verdict(repo, sha); v != "success" || len(e.ChecksIn(repo, sha)) != runsBefore {
		t.Fatalf("check changed after denied requests: verdict=%s\n%s", v, e.Dump())
	}
	if n := len(e.Audit(maint)); n != 0 {
		t.Fatalf("denied requests were audited (%d entries)\n%s", n, e.Dump())
	}

	// A maintainer cannot freeze the whole org either (owners only).
	e.expect(maint, http.StatusForbidden, http.MethodPost, "/api/v1/orgs/"+e.Org+"/freeze", map[string]string{"reason": "nope"})

	// A user with no access at all cannot even see the repo.
	outsider := stack.Login(t, e.Outsider)
	if st, _ := outsider.Do(http.MethodPost, e.repoPath(repo)+"/freeze", map[string]string{"reason": "nope"}); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("outsider freeze: status %d, want 403 or 404", st)
	}
	// And an anonymous caller is rejected.
	anon := stack.Anonymous(t)
	if st, _ := anon.Do(http.MethodPost, e.repoPath(repo)+"/freeze", map[string]string{"reason": "nope"}); st != http.StatusUnauthorized {
		t.Fatalf("anonymous freeze: status %d, want 401", st)
	}
}
