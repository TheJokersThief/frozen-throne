package slack

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type evRecorder struct {
	mu  sync.Mutex
	evs []Event
}

func (r *evRecorder) Notify(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func (r *evRecorder) take() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.evs
	r.evs = nil
	return out
}

type winEnv struct {
	t   *testing.T
	st  store.Store
	rec *evRecorder
	w   *WindowWatcher
}

var t0 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // a Thursday

func newWinEnv(t *testing.T) *winEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, err := range []error{
		st.UpsertInstallation(ctx, model.Installation{ID: 1, AccountLogin: "acme", AccountID: 100, AccountType: "Organization"}),
		st.UpsertRepo(ctx, model.Repo{ID: 10, InstallationID: 1, Owner: "acme", Name: "web", DefaultBranch: "main"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	rec := &evRecorder{}
	w := NewWindowWatcher(WindowOptions{Store: st, Notifier: rec, DashboardURL: "https://ft.example"})
	return &winEnv{t: t, st: st, rec: rec, w: w}
}

func (e *winEnv) tick(at time.Time) []Event {
	e.t.Helper()
	e.w.Tick(context.Background(), at)
	return e.rec.take()
}

func (e *winEnv) create(s model.Schedule) model.Schedule {
	e.t.Helper()
	s.InstallationID = 1
	s.Enabled = true
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	out, err := e.st.CreateSchedule(context.Background(), s)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func oneOff(name string, start, end time.Time, repo *int64) model.Schedule {
	return model.Schedule{Name: name, Reason: "quarter close", Kind: model.ScheduleOneOff, StartsAt: &start, EndsAt: &end, RepoID: repo}
}

func TestWindowStartAndEndOrgWide(t *testing.T) {
	e := newWinEnv(t)
	e.create(oneOff("Q-end", t0.Add(time.Hour), t0.Add(2*time.Hour), nil))

	if got := e.tick(t0); len(got) != 0 {
		t.Fatalf("seed tick emitted %v", got)
	}
	got := e.tick(t0.Add(90 * time.Minute))
	want := Event{
		InstallationID: 1, Kind: KindWindowStart, Org: "acme", Scope: "org", Schedule: "Q-end",
		Reason: "quarter close", DashboardURL: "https://ft.example/orgs/acme",
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("start = %+v, want %+v", got, want)
	}
	if got := e.tick(t0.Add(100 * time.Minute)); len(got) != 0 {
		t.Fatalf("repeat tick emitted %v", got)
	}
	got = e.tick(t0.Add(3 * time.Hour))
	want.Kind = KindWindowEnd
	if len(got) != 1 || got[0] != want {
		t.Fatalf("end = %+v, want %+v", got, want)
	}
	if got := e.tick(t0.Add(4 * time.Hour)); len(got) != 0 {
		t.Fatalf("after end emitted %v", got)
	}
}

func TestWindowRepoScope(t *testing.T) {
	e := newWinEnv(t)
	rid := int64(10)
	e.create(oneOff("web freeze", t0.Add(time.Hour), t0.Add(2*time.Hour), &rid))
	e.tick(t0)
	got := e.tick(t0.Add(90 * time.Minute))
	if len(got) != 1 || got[0].Scope != "repo" || got[0].Repo != "acme/web" || got[0].Org != "acme" ||
		got[0].DashboardURL != "https://ft.example/repos/acme/web" || got[0].Actor != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestWindowSeededActiveDoesNotNotifyButEnds(t *testing.T) {
	e := newWinEnv(t)
	e.create(oneOff("long", t0.Add(-time.Hour), t0.Add(time.Hour), nil))
	if got := e.tick(t0); len(got) != 0 {
		t.Fatalf("seed emitted %v", got)
	}
	got := e.tick(t0.Add(2 * time.Hour))
	if len(got) != 1 || got[0].Kind != KindWindowEnd {
		t.Fatalf("got %+v", got)
	}
}

func TestWindowRecurring(t *testing.T) {
	e := newWinEnv(t)
	e.create(model.Schedule{
		Name: "thursday lunch", Kind: model.ScheduleRecurring, Weekdays: model.WeekdaysOf(time.Thursday),
		StartTime: "13:00", EndTime: "14:00",
	})
	e.tick(t0)
	got := e.tick(t0.Add(90 * time.Minute))
	if len(got) != 1 || got[0].Kind != KindWindowStart {
		t.Fatalf("start = %+v", got)
	}
	got = e.tick(t0.Add(3 * time.Hour))
	if len(got) != 1 || got[0].Kind != KindWindowEnd {
		t.Fatalf("end = %+v", got)
	}
	// next week again
	if got := e.tick(t0.Add(7*24*time.Hour + 90*time.Minute)); len(got) != 1 || got[0].Kind != KindWindowStart {
		t.Fatalf("next week = %+v", got)
	}
}

func TestWindowDeletedWhileActive(t *testing.T) {
	e := newWinEnv(t)
	rid := int64(10)
	s := e.create(oneOff("gone", t0.Add(time.Hour), t0.Add(5*time.Hour), &rid))
	e.tick(t0)
	e.tick(t0.Add(90 * time.Minute))
	if err := e.st.DeleteSchedule(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	got := e.tick(t0.Add(100 * time.Minute))
	if len(got) != 1 || got[0].Kind != KindWindowEnd || got[0].Schedule != "gone" || got[0].Repo != "acme/web" {
		t.Fatalf("got %+v", got)
	}
	if got := e.tick(t0.Add(110 * time.Minute)); len(got) != 0 {
		t.Fatalf("repeat emitted %v", got)
	}
}

func TestWindowDisabledWhileActive(t *testing.T) {
	e := newWinEnv(t)
	s := e.create(oneOff("off", t0.Add(time.Hour), t0.Add(5*time.Hour), nil))
	e.tick(t0)
	e.tick(t0.Add(90 * time.Minute))
	s.Enabled = false
	if _, err := e.st.UpdateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got := e.tick(t0.Add(100 * time.Minute))
	if len(got) != 1 || got[0].Kind != KindWindowEnd || got[0].Schedule != "off" {
		t.Fatalf("got %+v", got)
	}
}

type flakyRepoStore struct {
	store.Store
	fail bool
}

func (f *flakyRepoStore) GetRepo(ctx context.Context, id int64) (model.Repo, error) {
	if f.fail {
		return model.Repo{}, context.DeadlineExceeded
	}
	return f.Store.GetRepo(ctx, id)
}

func TestWindowRetriesWhenRepoLookupFails(t *testing.T) {
	e := newWinEnv(t)
	fs := &flakyRepoStore{Store: e.st}
	e.w = NewWindowWatcher(WindowOptions{Store: fs, Notifier: e.rec})
	rid := int64(10)
	e.create(oneOff("web", t0.Add(time.Hour), t0.Add(5*time.Hour), &rid))
	e.tick(t0)
	fs.fail = true
	if got := e.tick(t0.Add(90 * time.Minute)); len(got) != 0 {
		t.Fatalf("emitted despite failed lookup: %v", got)
	}
	fs.fail = false
	got := e.tick(t0.Add(100 * time.Minute))
	if len(got) != 1 || got[0].Kind != KindWindowStart || got[0].Repo != "acme/web" {
		t.Fatalf("retry = %+v", got)
	}
}

func TestWindowEndMentionsOverlappingSchedule(t *testing.T) {
	e := newWinEnv(t)
	rid := int64(10)
	e.create(oneOff("org-wide", t0.Add(time.Hour), t0.Add(4*time.Hour), nil))
	e.create(oneOff("web-only", t0.Add(time.Hour), t0.Add(2*time.Hour), &rid))
	e.tick(t0)
	e.tick(t0.Add(90 * time.Minute)) // both start
	got := e.tick(t0.Add(150 * time.Minute))
	if len(got) != 1 || got[0].Schedule != "web-only" || got[0].Kind != KindWindowEnd || got[0].StillFrozenBy != "org-wide" {
		t.Fatalf("repo end = %+v", got)
	}
	got = e.tick(t0.Add(5 * time.Hour))
	if len(got) != 1 || got[0].Schedule != "org-wide" || got[0].StillFrozenBy != "" {
		t.Fatalf("org end = %+v", got)
	}
}

func TestWindowPicksUpRepoRename(t *testing.T) {
	e := newWinEnv(t)
	rid := int64(10)
	e.create(oneOff("web", t0.Add(time.Hour), t0.Add(2*time.Hour), &rid))
	e.tick(t0)
	e.tick(t0.Add(90 * time.Minute))
	if err := e.st.UpsertRepo(context.Background(), model.Repo{ID: 10, InstallationID: 1, Owner: "acme", Name: "site", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	got := e.tick(t0.Add(3 * time.Hour))
	if len(got) != 1 || got[0].Repo != "acme/site" {
		t.Fatalf("got %+v", got)
	}
}
