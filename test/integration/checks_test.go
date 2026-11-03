//go:build integration

package integration

import (
	"testing"

	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

func TestPROpenedNotFrozenSucceeds(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("head")
	e.OpenPR(7, sha)

	cr := e.WaitCheck(sha, nil)
	if cr.Name != CheckName || cr.Status != "completed" || cr.Conclusion != "success" {
		t.Fatalf("check run = %+v, want completed/success", cr)
	}
	if cr.HeadSHA != sha {
		t.Fatalf("head sha = %q, want %q", cr.HeadSHA, sha)
	}
}

func TestPROpenedWhileFrozenFails(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	maint := stack.Login(t, e.Maintainer)
	e.Freeze(maint, "release cut")

	sha := e.SHA("head")
	e.OpenPR(3, sha)
	cr := e.WaitCheck(sha, nil)
	if cr.Status != "completed" || cr.Conclusion != "failure" {
		t.Fatalf("check run = %+v, want completed/failure", cr)
	}
	if cr.Output.Summary == "" {
		t.Fatal("failure check should explain itself in the summary")
	}
}

func TestRerequestedRerunsAfterStateChange(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("head")
	e.OpenPR(5, sha)
	e.WaitCheck(sha, Conclusion("success"))

	maint := stack.Login(t, e.Maintainer)
	e.Freeze(maint, "incident")

	e.Deliver(webhooks.CheckRunRerequested(e.Params(5, sha)))
	cr := e.WaitCheck(sha, Conclusion("failure"))
	if cr.HeadSHA != sha {
		t.Fatalf("rerun on wrong sha: %+v", cr)
	}
}

func TestSynchronizeChecksTheNewSHA(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	first, second := e.SHA("first"), e.SHA("second")
	e.OpenPR(9, first)
	e.WaitCheck(first, Conclusion("success"))

	e.Deliver(webhooks.PullRequestSynchronize(e.Params(9, second)))
	cr := e.WaitCheck(second, Conclusion("success"))
	if cr.HeadSHA != second {
		t.Fatalf("check on %q, want %q", cr.HeadSHA, second)
	}
}

func TestReopenedChecksAgain(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("head")
	e.OpenPR(11, sha)
	e.WaitCheck(sha, Conclusion("success"))
	e.Deliver(webhooks.PullRequestClosed(e.Params(11, sha)))
	before := len(e.Checks(sha))

	maint := stack.Login(t, e.Maintainer)
	e.Freeze(maint, "freeze while closed")
	e.Deliver(webhooks.PullRequestReopened(e.Params(11, sha)))
	e.WaitCheck(sha, Conclusion("failure"))
	if len(e.Checks(sha)) < before {
		t.Fatalf("check runs disappeared: %+v", e.Checks(sha))
	}
}

func TestClosedAndMergedCreateNoCheck(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	closedSHA, mergedSHA := e.SHA("closed"), e.SHA("merged")

	e.Deliver(webhooks.PullRequestClosed(e.Params(21, closedSHA)))
	e.Deliver(webhooks.PullRequestMerged(e.Params(22, mergedSHA)))

	if runs := e.AllChecks(); len(runs) != 0 {
		t.Fatalf("closed/merged PRs produced check runs: %+v", runs)
	}
}

func TestMergeGroupChecksRequested(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("merge-group")
	e.Deliver(webhooks.MergeGroupChecksRequested(e.Params(13, sha)))

	cr := e.WaitCheck(sha, nil)
	if cr.Status != "completed" || cr.Conclusion != "success" {
		t.Fatalf("merge group check = %+v, want completed/success", cr)
	}

	maint := stack.Login(t, e.Maintainer)
	e.Freeze(maint, "queue paused")
	sha2 := e.SHA("merge-group-2")
	e.Deliver(webhooks.MergeGroupChecksRequested(e.Params(14, sha2)))
	e.WaitCheck(sha2, Conclusion("failure"))
}
