package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

func (e *env) window(startOff, endOff time.Duration) map[string]any {
	return map[string]any{
		"name": "release", "kind": "one_off", "timezone": "Europe/Dublin",
		"starts_at": e.now.Add(startOff).Format(time.RFC3339), "ends_at": e.now.Add(endOff).Format(time.RFC3339),
	}
}

func TestScheduleCRUDAndPermissions(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol, dave := e.login("alice"), e.login("bob"), e.login("carol"), e.login("dave")

	// Repo-scoped window: maintainer creates; member/none cannot.
	e.expect(e.do(carol, "POST", "/repos/acme/web/schedules", e.window(time.Hour, 2*time.Hour)), 403)
	e.expect(e.do(dave, "POST", "/repos/acme/web/schedules", e.window(time.Hour, 2*time.Hour)), 404)
	var sc scheduleDTO
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", e.window(time.Hour, 2*time.Hour)), 201).decode(&sc)
	if sc.ID == 0 || sc.Scope != "repo" || sc.Repo != "acme/web" || sc.Active || sc.NextStart == nil || !sc.Enabled || sc.CreatedBy != "bob" {
		t.Fatalf("created = %+v", sc)
	}
	if evs := e.rec.take(); len(evs) != 1 || evs[0].Action != model.AuditScheduleCreate || *evs[0].RepoID != 10 {
		t.Fatalf("events = %+v", evs)
	}

	// Org-wide windows need an org owner.
	e.expect(e.do(bob, "POST", "/orgs/acme/schedules", e.window(-time.Hour, time.Hour)), 403)
	var org scheduleDTO
	e.expect(e.do(alice, "POST", "/orgs/acme/schedules", e.window(-time.Hour, time.Hour)), 201).decode(&org)
	if org.Scope != "org" || org.Repo != "" || !org.Active || org.NextEnd == nil {
		t.Fatalf("org window = %+v", org)
	}

	// The active org window freezes web (alongside nothing else).
	d := e.detail(bob, "/repos/acme/web")
	if !d.State.Frozen || d.State.Reasons[0].Kind != "schedule" || d.State.Reasons[0].ScheduleID != org.ID || d.State.NextChange == nil {
		t.Fatalf("state = %+v", d.State)
	}
	if len(d.Schedules) != 2 {
		t.Fatalf("detail schedules = %d", len(d.Schedules))
	}

	// Lists: repo list has only its own; org list only org-wide.
	var l struct{ Items []scheduleDTO }
	e.expect(e.do(carol, "GET", "/repos/acme/web/schedules", nil), 200).decode(&l)
	if len(l.Items) != 1 || l.Items[0].ID != sc.ID {
		t.Fatalf("repo list = %+v", l)
	}
	e.expect(e.do(carol, "GET", "/orgs/acme/schedules", nil), 200).decode(&l)
	if len(l.Items) != 1 || l.Items[0].ID != org.ID {
		t.Fatalf("org list = %+v", l)
	}
	e.expect(e.do(dave, "GET", "/orgs/acme/schedules", nil), 404)

	// Get / update / delete.
	path := fmt.Sprintf("/schedules/%d", sc.ID)
	e.expect(e.do(carol, "GET", path, nil), 200)
	e.expect(e.do(dave, "GET", path, nil), 404)
	e.expect(e.do(carol, "PUT", path, e.window(0, time.Hour)), 403)
	upd := e.window(-time.Minute, time.Hour)
	upd["enabled"] = false
	e.expect(e.do(bob, "PUT", path, upd), 200).decode(&sc)
	if sc.Enabled || sc.Active || sc.Scope != "repo" {
		t.Fatalf("updated = %+v", sc)
	}
	e.expect(e.do(bob, "PUT", fmt.Sprintf("/schedules/%d", org.ID), upd), 403) // maintainer vs org window
	e.expect(e.do(bob, "DELETE", fmt.Sprintf("/schedules/%d", org.ID), nil), 403)
	e.expect(e.do(bob, "DELETE", path, nil), 204)
	e.expect(e.do(bob, "GET", path, nil), 404)
	e.expect(e.do(bob, "GET", "/schedules/abc", nil), 404)

	// Deleting the active org window thaws web.
	e.expect(e.do(alice, "DELETE", fmt.Sprintf("/schedules/%d", org.ID), nil), 204)
	if e.detail(bob, "/repos/acme/web").State.Frozen {
		t.Fatal("web should be thawed after deleting the active window")
	}

	var actions []model.AuditAction
	for _, a := range e.audit() {
		actions = append(actions, a.Action)
	}
	want := []model.AuditAction{model.AuditScheduleDelete, model.AuditScheduleDelete, model.AuditScheduleUpdate, model.AuditScheduleCreate, model.AuditScheduleCreate}
	if fmt.Sprint(actions) != fmt.Sprint(want) {
		t.Fatalf("audit actions = %v, want %v", actions, want)
	}
}

func TestScheduleValidation(t *testing.T) {
	e := newEnv(t)
	bob := e.login("bob")
	rec := func(m map[string]any) map[string]any {
		base := map[string]any{"kind": "recurring", "timezone": "UTC", "weekdays": []string{"fri"}, "start_time": "17:00", "end_time": "09:00"}
		for k, v := range m {
			if v == nil {
				delete(base, k)
			} else {
				base[k] = v
			}
		}
		return base
	}
	bad := map[string]map[string]any{
		"kind":       {"kind": "weekly", "timezone": "UTC"},
		"timezone":   rec(map[string]any{"timezone": "Mars/Base"}),
		"weekdays":   rec(map[string]any{"weekdays": []string{"funday"}}),
		"start_time": rec(map[string]any{"start_time": "25:00"}),
		"end_time":   rec(map[string]any{"end_time": nil}),
		"valid_from": rec(map[string]any{"valid_from": "tomorrow"}),
		"ends_at":    {"kind": "one_off", "timezone": "UTC", "starts_at": "2026-02-02T10:00:00Z", "ends_at": "2026-02-02T09:00:00Z"},
		"starts_at":  {"kind": "one_off", "timezone": "UTC"},
	}
	for field, body := range bad {
		var er errorBody
		e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", body), 400).decode(&er)
		if er.Fields[field] == "" {
			t.Errorf("%s: fields = %+v", field, er.Fields)
		}
	}
	rb := rec(map[string]any{"weekdays": []string{"fri", "fri"}})
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", rb), 400)
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", rec(map[string]any{"valid_from": "2026-03-01", "valid_until": "2026-02-01"})), 400)
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", rec(map[string]any{"bogus": 1})), 400) // unknown field

	var sc scheduleDTO
	e.expect(e.do(bob, "POST", "/repos/acme/web/schedules", rec(map[string]any{"weekdays": []string{"sat", "fri"}, "valid_from": "2026-01-01"})), 201).decode(&sc)
	if fmt.Sprint(sc.Weekdays) != "[fri sat]" || sc.StartTime != "17:00" || sc.ValidFrom == nil || *sc.ValidFrom != "2026-01-01" {
		t.Fatalf("recurring = %+v", sc)
	}
}

func TestBypassLifecycle(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.login("alice"), e.login("bob"), e.login("carol")
	byp := "/repos/acme/web/pulls/7/bypass"

	e.expect(e.do(carol, "POST", byp, `{"reason":"hotfix"}`), 403)
	e.expect(e.do(bob, "POST", byp, `{"reason":"   "}`), 400)
	e.expect(e.do(bob, "POST", byp, `{}`), 400)
	e.expect(e.do(bob, "POST", "/repos/acme/web/pulls/0/bypass", `{"reason":"x"}`), 400)

	var b bypassDTO
	e.expect(e.do(bob, "POST", byp, `{"reason":" hotfix for outage "}`), 201).decode(&b)
	if b.PRNumber != 7 || b.Reason != "hotfix for outage" || b.Repo != "acme/web" || b.CreatedBy != "bob" {
		t.Fatalf("bypass = %+v", b)
	}
	if evs := e.rec.take(); len(evs) != 1 || evs[0].PRNumber != 7 || evs[0].Action != model.AuditBypassCreate {
		t.Fatalf("events = %+v", evs)
	}
	e.expect(e.do(bob, "POST", byp, `{"reason":"again"}`), 409)

	var l struct{ Items []bypassDTO }
	e.expect(e.do(carol, "GET", "/repos/acme/web/bypasses", nil), 200).decode(&l)
	if len(l.Items) != 1 {
		t.Fatalf("bypasses = %+v", l)
	}
	if d := e.detail(carol, "/repos/acme/web"); len(d.Bypasses) != 1 {
		t.Fatalf("detail bypasses = %+v", d.Bypasses)
	}

	e.expect(e.do(bob, "DELETE", byp, nil), 204)
	e.expect(e.do(bob, "DELETE", byp, nil), 404)
	e.expect(e.do(carol, "GET", "/repos/acme/web/bypasses", nil), 200).decode(&l)
	if len(l.Items) != 0 {
		t.Fatalf("active bypasses = %+v", l)
	}
	e.expect(e.do(carol, "GET", "/repos/acme/web/bypasses?active=false", nil), 200).decode(&l)
	if len(l.Items) != 1 || l.Items[0].EndReason != "revoked" || l.Items[0].EndedAt == nil {
		t.Fatalf("history = %+v", l)
	}

	// The bypass is audited with its reason.
	var found bool
	for _, a := range e.audit() {
		if a.Action == model.AuditBypassCreate && a.Reason == "hotfix for outage" && a.PRNumber != nil && *a.PRNumber == 7 && a.ActorLogin == "bob" {
			found = true
		}
	}
	if !found {
		t.Fatal("bypass.create not audited")
	}
	_ = alice
}

func TestThawClearsBypassesOnlyWhenFullyThawed(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.login("alice"), e.login("bob")
	ctx := context.Background()
	e.expect(e.do(alice, "POST", "/orgs/acme/freeze", `{}`), 201)
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{}`), 201)
	e.expect(e.do(bob, "POST", "/repos/acme/web/pulls/3/bypass", `{"reason":"ok"}`), 201)

	// Repo thaw while the org freeze still applies: bypass survives.
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	if _, err := e.st.ActiveBypass(ctx, 10, 3); err != nil {
		t.Fatalf("bypass should survive: %v", err)
	}
	// Org thaw: no cause left, bypass is cleared with reason thawed.
	e.expect(e.do(alice, "DELETE", "/orgs/acme/freeze", nil), 200)
	if _, err := e.st.ActiveBypass(ctx, 10, 3); err == nil {
		t.Fatal("bypass should have ended")
	}
	hist, _ := e.st.ListBypasses(ctx, store.BypassFilter{RepoID: ptr(int64(10))})
	if len(hist) != 1 || hist[0].EndReason != model.BypassEndedThawed {
		t.Fatalf("history = %+v", hist)
	}
	var ended bool
	for _, a := range e.audit() {
		ended = ended || a.Action == model.AuditBypassEnd && a.Details == `{"end_reason":"thawed"}`
	}
	if !ended {
		t.Fatal("bypass.end not audited")
	}
}
