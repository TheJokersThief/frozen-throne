package webhooks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func send(t *testing.T, r *receiver, d webhooks.Delivery, opts ...webhooks.Option) webhooks.Response {
	t.Helper()
	s := webhooks.NewSender(r.URL, []byte(secret), opts...)
	resp, err := s.Send(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// newInstalledReceiver mimics GitHub's ordering: the installation event
// arrives before repo-level events (those carry only a thin installation).
func newInstalledReceiver(t *testing.T) *receiver {
	t.Helper()
	r := newReceiver(t)
	mustAccept(t, r, webhooks.InstallationCreated(webhooks.Params{}))
	return r
}

func mustAccept(t *testing.T, r *receiver, d webhooks.Delivery) {
	t.Helper()
	if resp := send(t, r, d); resp.Status != 204 {
		t.Fatalf("%s: status %d body %q", d.Event, resp.Status, resp.Body)
	}
}

func TestPullRequestActionsCreateCheckOnHeadSHA(t *testing.T) {
	p := webhooks.Params{PRNumber: 7, HeadSHA: sha}
	cases := map[string]webhooks.Delivery{
		"opened":      webhooks.PullRequestOpened(p),
		"synchronize": webhooks.PullRequestSynchronize(p),
		"reopened":    webhooks.PullRequestReopened(p),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			r := newInstalledReceiver(t)
			mustAccept(t, r, d)
			cs := r.API.creates()
			if len(cs) != 1 || cs[0].HeadSHA != sha {
				t.Fatalf("check runs = %+v", cs)
			}
			repo, err := r.Store.GetRepoByName(context.Background(), "acme", "widgets")
			if err != nil || repo.ID == 0 {
				t.Fatalf("repo not stored: %v", err)
			}
		})
	}
}

func TestPullRequestClosedAccepted(t *testing.T) {
	for name, d := range map[string]webhooks.Delivery{
		"unmerged": webhooks.PullRequestClosed(webhooks.Params{PRNumber: 7, HeadSHA: sha}),
		"merged":   webhooks.PullRequestMerged(webhooks.Params{PRNumber: 7, HeadSHA: sha}),
	} {
		t.Run(name, func(t *testing.T) {
			r := newInstalledReceiver(t)
			mustAccept(t, r, webhooks.PullRequestOpened(webhooks.Params{PRNumber: 7, HeadSHA: sha}))
			mustAccept(t, r, d)
		})
	}
}

func TestDraftParam(t *testing.T) {
	d := webhooks.PullRequestOpened(webhooks.Params{PRNumber: 1, HeadSHA: sha, Draft: true})
	if !contains(d.Body, `"draft": true`) {
		t.Fatalf("draft not rendered: %s", d.Body)
	}
}

func TestCheckRunRerequested(t *testing.T) {
	r := newInstalledReceiver(t)
	mustAccept(t, r, webhooks.CheckRunRerequested(webhooks.Params{PRNumber: 7, HeadSHA: sha}))
	cs := r.API.creates()
	if len(cs) != 1 || cs[0].HeadSHA != sha {
		t.Fatalf("check runs = %+v", cs)
	}
}

func TestMergeGroupChecksRequested(t *testing.T) {
	r := newInstalledReceiver(t)
	mg := "fedcba9876543210fedcba9876543210fedcba98"
	mustAccept(t, r, webhooks.MergeGroupChecksRequested(webhooks.Params{PRNumber: 7, HeadSHA: mg}))
	cs := r.API.creates()
	if len(cs) != 1 || cs[0].HeadSHA != mg {
		t.Fatalf("check runs = %+v", cs)
	}
}

func TestInstallationCreatedAndDeleted(t *testing.T) {
	r := newReceiver(t)
	ctx := context.Background()
	p := webhooks.Params{InstallationID: 99, RepoID: 555}
	mustAccept(t, r, webhooks.InstallationCreated(p))
	inst, err := r.Store.GetInstallation(ctx, 99)
	if err != nil || inst.AccountLogin != "acme" {
		t.Fatalf("installation = %+v, %v", inst, err)
	}
	repo, err := r.Store.GetRepo(ctx, 555)
	if err != nil || repo.Owner != "acme" || repo.Name != "widgets" || repo.InstallationID != 99 {
		t.Fatalf("repo = %+v, %v", repo, err)
	}
	mustAccept(t, r, webhooks.InstallationDeleted(p))
	if _, err := r.Store.GetInstallation(ctx, 99); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("installation still present: %v", err)
	}
	if _, err := r.Store.GetRepo(ctx, 555); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("repo still present: %v", err)
	}
}

func TestInstallationRepositoriesAddedAndRemoved(t *testing.T) {
	r := newReceiver(t)
	ctx := context.Background()
	p := webhooks.Params{InstallationID: 99, RepoID: 555}
	mustAccept(t, r, webhooks.InstallationRepositoriesAdded(p))
	repo, err := r.Store.GetRepo(ctx, 555)
	if err != nil || repo.RemovedAt != nil {
		t.Fatalf("repo = %+v, %v", repo, err)
	}
	mustAccept(t, r, webhooks.InstallationRepositoriesRemoved(p))
	repo, err = r.Store.GetRepo(ctx, 555)
	if err != nil || repo.RemovedAt == nil {
		t.Fatalf("repo not marked removed: %+v, %v", repo, err)
	}
}

func TestPingAndUnknownHaveNoEffect(t *testing.T) {
	for name, d := range map[string]webhooks.Delivery{
		"ping":    webhooks.Ping(webhooks.Params{}),
		"unknown": webhooks.StarCreated(webhooks.Params{}),
	} {
		t.Run(name, func(t *testing.T) {
			r := newReceiver(t)
			mustAccept(t, r, d)
			if n := len(r.API.creates()); n != 0 {
				t.Fatalf("%d check runs", n)
			}
			if is, _ := r.Store.ListInstallations(context.Background()); len(is) != 0 {
				t.Fatalf("installations stored: %v", is)
			}
		})
	}
}

func TestBadSignaturesRejected(t *testing.T) {
	d := webhooks.PullRequestOpened(webhooks.Params{PRNumber: 7, HeadSHA: sha})
	for name, opt := range map[string]webhooks.Option{
		"invalid": webhooks.WithInvalidSignature(),
		"missing": webhooks.WithoutSignature(),
	} {
		t.Run(name, func(t *testing.T) {
			r := newReceiver(t)
			if resp := send(t, r, d, opt); resp.Status != 401 {
				t.Fatalf("status %d", resp.Status)
			}
			if n := len(r.API.creates()); n != 0 {
				t.Fatalf("%d check runs", n)
			}
		})
	}
}

func TestRenderGeneric(t *testing.T) {
	if _, err := webhooks.Render("nope", webhooks.Params{}); err == nil {
		t.Fatal("expected error for unknown fixture")
	}
	d, err := webhooks.Render("pull_request.opened", webhooks.Params{PRNumber: 3, HeadSHA: sha})
	if err != nil || d.Event != "pull_request" {
		t.Fatalf("%+v %v", d, err)
	}
}

func contains(b []byte, s string) bool {
	return len(s) == 0 || indexOf(string(b), s) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
