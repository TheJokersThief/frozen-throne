package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

func (e *env) audit() []model.AuditEntry {
	e.t.Helper()
	l, err := e.st.ListAudit(context.Background(), store.AuditFilter{Limit: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	return l // newest first
}

func (e *env) detail(c *http.Cookie, path string) repoDetailDTO {
	e.t.Helper()
	var d repoDetailDTO
	e.expect(e.do(c, "GET", path, nil), 200).decode(&d)
	return d
}

func TestRepoVisibilityAndRoles(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol, dave := e.login("alice"), e.login("bob"), e.login("carol"), e.login("dave")

	// dave sees nothing: 404, never 403.
	e.expect(e.do(dave, "GET", "/repos/acme/web", nil), 404)
	e.expect(e.do(dave, "POST", "/repos/acme/web/freeze", `{}`), 404)
	e.expect(e.do(alice, "GET", "/repos/acme/nope", nil), 404)

	// carol is a member: can read, cannot freeze.
	d := e.detail(carol, "/repos/acme/web")
	if d.Permissions.CanFreeze || d.FullName != "acme/web" {
		t.Fatalf("carol detail = %+v", d)
	}
	e.expect(e.do(carol, "POST", "/repos/acme/web/freeze", `{}`), 403)
	// bob maintains web only; api is invisible to him.
	e.expect(e.do(bob, "GET", "/repos/acme/api", nil), 404)
	if !e.detail(bob, "/repos/acme/web").Permissions.CanFreeze {
		t.Fatal("bob should be able to freeze web")
	}
	// alice (org owner) can do everything.
	if !e.detail(alice, "/repos/acme/api").Permissions.CanFreeze {
		t.Fatal("owner should be able to freeze api")
	}
}

func TestFreezeAndThawRepo(t *testing.T) {
	e := newEnv(t)
	bob := e.login("bob")

	var res freezeResultDTO
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", map[string]string{"reason": "release cut"}), 201).decode(&res)
	if !res.State.Frozen || len(res.State.Reasons) != 1 || res.State.Reasons[0].Kind != "repo_freeze" ||
		res.State.Reasons[0].Text != "release cut" || res.State.Reasons[0].By != "bob" {
		t.Fatalf("state = %+v", res.State)
	}
	if evs := e.rec.take(); len(evs) != 1 || evs[0].RepoID == nil || *evs[0].RepoID != 10 || evs[0].Action != model.AuditFreeze {
		t.Fatalf("events = %+v", evs)
	}

	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{}`), 409)
	if len(e.rec.take()) != 0 {
		t.Fatal("conflict must not emit an event")
	}
	if !e.detail(bob, "/repos/acme/web").State.Frozen {
		t.Fatal("detail should be frozen")
	}
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{"reason": 5}`), 400)

	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200).decode(&res)
	if res.State.Frozen {
		t.Fatalf("still frozen: %+v", res.State)
	}
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 404)

	log := e.audit()
	if len(log) != 2 || log[0].Action != model.AuditThaw || log[1].Action != model.AuditFreeze ||
		log[1].ActorLogin != "bob" || log[1].Reason != "release cut" || log[1].Scope != model.FreezeScopeRepo {
		t.Fatalf("audit = %+v", log)
	}
}

func TestFreezeRepoReasonTooLong(t *testing.T) {
	e := newEnv(t)
	r := e.do(e.login("bob"), "POST", "/repos/acme/web/freeze", map[string]string{"reason": string(make([]byte, 501))})
	e.expect(r, 400)
}

func TestOrgFreezeIndependentOfRepoFreeze(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.login("alice"), e.login("bob")

	// Maintainers cannot manage org-wide freezes; non-members can't see the org.
	e.expect(e.do(bob, "POST", "/orgs/acme/freeze", `{}`), 403)
	e.expect(e.do(e.login("dave"), "POST", "/orgs/acme/freeze", `{}`), 404)

	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{"reason":"repo"}`), 201)
	var o orgFreezeResultDTO
	e.expect(e.do(alice, "POST", "/orgs/acme/freeze", `{"reason":"org"}`), 201).decode(&o)
	if !o.Org.State.Frozen || o.Org.RepoCount != 2 || o.Org.State.Reasons[0].Kind != "org_freeze" {
		t.Fatalf("org = %+v", o.Org)
	}
	e.expect(e.do(alice, "POST", "/orgs/acme/freeze", `{}`), 409)

	// Both causes apply to web; api is frozen only by the org freeze (also
	// covers repos added later, since the freeze is per installation).
	if got := len(e.detail(bob, "/repos/acme/web").State.Reasons); got != 2 {
		t.Fatalf("web reasons = %d", got)
	}
	// Thawing the repo freeze does not thaw the repo.
	var res freezeResultDTO
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200).decode(&res)
	if !res.State.Frozen || res.State.Reasons[0].Kind != "org_freeze" {
		t.Fatalf("after repo thaw = %+v", res.State)
	}
	// Thawing the org leaves a repo with its own freeze frozen.
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{}`), 201)
	e.expect(e.do(alice, "DELETE", "/orgs/acme/freeze", nil), 200).decode(&o)
	if o.Org.State.Frozen {
		t.Fatal("org still frozen")
	}
	if !e.detail(bob, "/repos/acme/web").State.Frozen {
		t.Fatal("web should remain frozen by its own freeze")
	}
	if e.detail(alice, "/repos/acme/api").State.Frozen {
		t.Fatal("api should be thawed")
	}
	e.expect(e.do(alice, "DELETE", "/orgs/acme/freeze", nil), 404)

	// Org-wide events have no repo.
	for _, ev := range e.rec.take() {
		if ev.Action == model.AuditFreeze && ev.RepoID == nil && ev.InstallationID != 1 {
			t.Fatalf("bad org event %+v", ev)
		}
	}
}

func TestRechecking(t *testing.T) {
	e := newEnv(t)
	for n := 1; n <= 3; n++ {
		if err := e.st.UpsertPullRequest(context.Background(), model.PullRequest{RepoID: 10, Number: n, State: model.PROpen, Conclusion: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	var res freezeResultDTO
	e.expect(e.do(e.login("bob"), "POST", "/repos/acme/web/freeze", `{}`), 201).decode(&res)
	if res.Rechecking != 3 {
		t.Fatalf("rechecking = %d", res.Rechecking)
	}
}

func TestListReposAndOrgs(t *testing.T) {
	e := newEnv(t)
	alice, bob, dave := e.login("alice"), e.login("bob"), e.login("dave")
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{}`), 201)

	var list struct {
		Items []repoSummaryDTO `json:"items"`
		Total int              `json:"total"`
	}
	e.expect(e.do(alice, "GET", "/repos", nil), 200).decode(&list)
	if list.Total != 2 || list.Items[0].FullName != "acme/api" || list.Items[1].FullName != "acme/web" {
		t.Fatalf("alice list = %+v", list)
	}
	e.expect(e.do(bob, "GET", "/repos", nil), 200).decode(&list)
	if list.Total != 1 || list.Items[0].FullName != "acme/web" || !list.Items[0].State.Frozen {
		t.Fatalf("bob list = %+v", list)
	}
	e.expect(e.do(dave, "GET", "/repos", nil), 200).decode(&list)
	if list.Total != 0 || list.Items == nil {
		t.Fatalf("dave list = %+v", list)
	}
	e.expect(e.do(alice, "GET", "/repos?state=frozen", nil), 200).decode(&list)
	if list.Total != 1 || list.Items[0].Name != "web" {
		t.Fatalf("frozen filter = %+v", list)
	}
	e.expect(e.do(alice, "GET", "/repos?state=thawed&q=API", nil), 200).decode(&list)
	if list.Total != 1 || list.Items[0].Name != "api" {
		t.Fatalf("thawed+q filter = %+v", list)
	}
	e.expect(e.do(alice, "GET", "/repos?limit=1&offset=1", nil), 200).decode(&list)
	if list.Total != 2 || len(list.Items) != 1 || list.Items[0].Name != "web" {
		t.Fatalf("paging = %+v", list)
	}
	e.expect(e.do(alice, "GET", "/repos?org=other", nil), 200).decode(&list)
	if list.Total != 0 {
		t.Fatalf("other org = %+v", list)
	}
	e.expect(e.do(alice, "GET", "/repos?limit=0", nil), 400)
	e.expect(e.do(alice, "GET", "/repos?state=maybe", nil), 400)

	// Orgs: bob has repo access only (member), dave sees nothing.
	var orgs struct{ Items []orgDTO }
	e.expect(e.do(bob, "GET", "/orgs", nil), 200).decode(&orgs)
	if len(orgs.Items) != 1 || orgs.Items[0].Permissions.CanFreeze {
		t.Fatalf("bob orgs = %+v", orgs)
	}
	e.expect(e.do(dave, "GET", "/orgs", nil), 200).decode(&orgs)
	if len(orgs.Items) != 0 {
		t.Fatalf("dave orgs = %+v", orgs)
	}
	var o orgDTO
	e.expect(e.do(alice, "GET", "/orgs/acme", nil), 200).decode(&o)
	if !o.Permissions.CanFreeze || o.Permissions.CanManageSlack == nil || !*o.Permissions.CanManageSlack {
		t.Fatalf("alice org perms = %+v", o.Permissions)
	}
	e.expect(e.do(dave, "GET", "/orgs/acme", nil), 404)
}

func TestPermissionErrors(t *testing.T) {
	e := newEnv(t)
	e.perms.err = ErrTokenInvalid
	c := e.login("alice")
	e.expect(e.do(c, "GET", "/repos/acme/web", nil), 401)
	// The session is dropped when GitHub rejects the token.
	e.perms.err = nil
	e.expect(e.do(c, "GET", "/auth/me", nil), 401)
}

func TestSlackStubbed(t *testing.T) {
	e := newEnv(t)
	c := e.login("alice")
	e.expect(e.do(c, "GET", "/orgs/acme/slack", nil), http.StatusNotImplemented)
	e.expect(e.do(c, "POST", "/orgs/acme/slack/test", nil), http.StatusNotImplemented)
}
