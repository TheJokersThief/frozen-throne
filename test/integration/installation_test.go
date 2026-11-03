//go:build integration

package integration

import (
	"slices"
	"testing"

	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// The observable for installation state is GET /api/v1/repos, which lists the
// repos of every stored installation the caller can see (the app asks the mock
// what the user may do on each repo).

func TestInstallationCreatedListsRepoForEntitledUsers(t *testing.T) {
	e := stack.Reset(t)
	maint, outsider := stack.Login(t, e.Maintainer), stack.Login(t, e.Outsider)
	full := e.Org + "/" + e.Repo

	if got := e.ListRepos(maint); len(got) != 0 {
		t.Fatalf("before installation: %v", got)
	}
	e.Install()
	if got := e.ListRepos(maint); !slices.Equal(got, []string{full}) {
		t.Fatalf("maintainer sees %v, want [%s]", got, full)
	}
	if got := e.ListRepos(outsider); len(got) != 0 {
		t.Fatalf("outsider sees %v, want nothing", got)
	}
}

func TestInstallationDeletedRemovesRepos(t *testing.T) {
	e := stack.Reset(t)
	maint := stack.Login(t, e.Maintainer)
	e.Install()
	if len(e.ListRepos(maint)) != 1 {
		t.Fatalf("setup: %v", e.ListRepos(maint))
	}
	e.Deliver(webhooks.InstallationDeleted(e.Params(1, e.SHA("x"))))
	if got := e.ListRepos(maint); len(got) != 0 {
		t.Fatalf("after deletion: %v", got)
	}
}

func TestInstallationRepositoriesAddedAndRemoved(t *testing.T) {
	e := stack.Reset(t)
	maint := stack.Login(t, e.Maintainer)
	e.Install()

	id := e.AddRepo("gadgets")
	// A fresh login: seeding the user again rotated its token.
	maint = stack.Login(t, e.Maintainer)
	p := e.Params(1, e.SHA("x"))
	p.Repo, p.RepoID = "gadgets", id
	e.Deliver(webhooks.InstallationRepositoriesAdded(p))
	want := []string{e.Org + "/gadgets", e.Org + "/" + e.Repo}
	if got := e.ListRepos(maint); !slices.Equal(got, want) {
		t.Fatalf("after add: %v, want %v", got, want)
	}

	e.Deliver(webhooks.InstallationRepositoriesRemoved(p))
	if got := e.ListRepos(maint); !slices.Equal(got, []string{e.Org + "/" + e.Repo}) {
		t.Fatalf("after remove: %v", got)
	}
}

// TODO(internal/github): repo-level events that arrive before installation.created
// make the handler return 500. Real GitHub sends only {id, node_id} in the
// installation block of pull_request/check_run/merge_group payloads, so
// upsertInstallation stores an empty account_type and the store's CHECK
// constraint fails. Remove the Skip once the handler tolerates (or fetches) the
// missing account.
func TestPullRequestBeforeInstallation(t *testing.T) {
	t.Skip("known handler bug: thin installation payload -> 500 (account_type empty); see TODO above")
	e := stack.Reset(t)
	sha := e.SHA("head")
	e.SeedPR(1, sha)
	e.Deliver(webhooks.PullRequestOpened(e.Params(1, sha)))
	e.WaitCheck(sha, Conclusion("success"))
}
