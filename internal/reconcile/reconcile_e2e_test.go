package reconcile

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// fakeGH is an in-memory github.API that tracks Check Runs per repo and head
// SHA, counts calls and can inject errors or block listing.
type fakeGH struct {
	mu     sync.Mutex
	prs    map[string][]github.PullSummary // "owner/name" -> open PRs
	checks map[int64]*ghCheck
	byKey  map[string]int64 // repo|sha -> latest check id
	nextID int64
	lists  map[string]int
	writes int
	// injectList, if set, is consulted on every ListOpenPullRequests.
	injectList func(installationID int64, repo string) error
	gate       chan struct{} // if non-nil, ListOpenPullRequests blocks until closed
	entered    int           // list calls that reached the gate
}

type ghCheck struct{ repo, sha, conclusion string }

func newFakeGH() *fakeGH {
	return &fakeGH{prs: map[string][]github.PullSummary{}, checks: map[int64]*ghCheck{}, byKey: map[string]int64{}, lists: map[string]int{}}
}

func (g *fakeGH) addPR(repo string, n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prs[repo] = append(g.prs[repo], github.PullSummary{Number: n, HeadSHA: fmt.Sprintf("sha-%s-%d", repo, n)})
}

func (g *fakeGH) CreateCheckRun(_ context.Context, _ int64, owner, repo string, c github.CheckRun) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextID++
	g.writes++
	name := owner + "/" + repo
	g.checks[g.nextID] = &ghCheck{name, c.HeadSHA, c.Conclusion}
	g.byKey[name+"|"+c.HeadSHA] = g.nextID
	return g.nextID, nil
}

func (g *fakeGH) UpdateCheckRun(_ context.Context, _ int64, _, _ string, id int64, c github.CheckRun) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	ck, ok := g.checks[id]
	if !ok {
		return &github.HTTPError{Status: 404, Method: "PATCH"}
	}
	g.writes++
	ck.conclusion = c.Conclusion
	return nil
}

func (g *fakeGH) ListOpenPullRequests(_ context.Context, inst int64, owner, repo string) ([]github.PullSummary, error) {
	name := owner + "/" + repo
	g.mu.Lock()
	g.lists[name]++
	inj, gate := g.injectList, g.gate
	if gate != nil {
		g.entered++
	}
	g.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if inj != nil {
		if err := inj(inst, name); err != nil {
			return nil, err
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]github.PullSummary(nil), g.prs[name]...), nil
}

func (g *fakeGH) conclusion(repo string, n int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.byKey[fmt.Sprintf("%s|sha-%s-%d", repo, repo, n)]
	if !ok {
		return ""
	}
	return g.checks[id].conclusion
}

func (g *fakeGH) listCount(repo string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lists[repo]
}

func (g *fakeGH) totalLists() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.lists {
		n += c
	}
	return n
}

func (g *fakeGH) setGate(ch chan struct{}) {
	g.mu.Lock()
	g.gate = ch
	g.entered = 0
	g.mu.Unlock()
}

func (g *fakeGH) enteredCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.entered
}

func (g *fakeGH) setInject(f func(int64, string) error) {
	g.mu.Lock()
	g.injectList = f
	g.mu.Unlock()
}

type e2e struct {
	t    *testing.T
	st   *store.SQLite
	clk  *fakeClock
	gh   *fakeGH
	svc  *github.Service
	r    *Reconciler
	done chan error
	stop context.CancelFunc
}

// newE2E seeds installations (id -> repo names) with one open PR (#1) per repo
// and starts a Reconciler on the fake clock.
func newE2E(t *testing.T, insts map[int64][]string, mod func(*Config)) *e2e {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &e2e{t: t, st: st, clk: newFakeClock(start0), gh: newFakeGH()}
	for id, names := range insts {
		if err := st.UpsertInstallation(ctx, model.Installation{ID: id, AccountLogin: fmt.Sprint("org", id), AccountType: "Organization"}); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			e.addRepo(id, n)
		}
	}
	eng := engine.New()
	e.svc, err = github.NewService(github.ServiceConfig{Store: st, Engine: eng, API: e.gh, WebhookSecret: []byte("s"), Now: e.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Store: st, Reevaluator: e.svc, Engine: eng, Clock: e.clk, Interval: 5 * time.Minute}
	if mod != nil {
		mod(&cfg)
	}
	e.r, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	e.stop = cancel
	e.done = make(chan error, 1)
	go func() { e.done <- e.r.Run(rctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-e.done:
		case <-time.After(5 * time.Second):
		}
	})
	return e
}

var repoSeq int64

// addRepo registers a repo (id derived from name) with open PR #1 and
// returns its ID. The Service is not told: this is what a missed webhook
// looks like.
func (e *e2e) addRepo(inst int64, name string) int64 {
	e.t.Helper()
	repoSeq++
	id := inst*1000 + repoSeq
	if err := e.st.UpsertRepo(context.Background(), model.Repo{ID: id, InstallationID: inst, Owner: "o", Name: name, DefaultBranch: "main"}); err != nil {
		e.t.Fatal(err)
	}
	e.gh.addPR("o/"+name, 1)
	return id
}

func (e *e2e) wantConclusion(repo string, n int, want string) {
	e.t.Helper()
	eventually(e.t, func() bool { return e.gh.conclusion("o/"+repo, n) == want })
}

// settle waits for the initial pass to have written every named repo's check.
func (e *e2e) settle(repos ...string) {
	e.t.Helper()
	for _, r := range repos {
		e.wantConclusion(r, 1, "success")
	}
	eventually(e.t, func() bool { return e.r.q.len() == 0 })
}

func (e *e2e) orgFreeze(inst int64) model.Freeze {
	f, err := e.st.CreateFreeze(context.Background(), model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: inst, Reason: "release", CreatedBy: "u"})
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *e2e) repoID(name string) int64 {
	e.t.Helper()
	r, err := e.st.GetRepoByName(context.Background(), "o", name)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.ID
}

func TestE2ERepoFreezeAndThaw(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a", "b"}}, nil)
	for _, n := range []string{"a", "b"} {
		e.gh.addPR("o/"+n, 2)
	}
	e.settle("a", "b")
	id := e.repoID("a")
	f, err := e.st.CreateFreeze(context.Background(), model.Freeze{Scope: model.FreezeScopeRepo, InstallationID: 1, RepoID: &id, Reason: "x", CreatedBy: "u"})
	if err != nil {
		t.Fatal(err)
	}
	e.r.Repo(id)
	e.wantConclusion("a", 1, "failure")
	e.wantConclusion("a", 2, "failure")
	consistently(t, func() bool { return e.gh.conclusion("o/b", 1) == "success" })

	if _, err := e.st.EndFreeze(context.Background(), f.ID, store.ThawInfo{By: "u", At: e.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	e.r.Repo(id)
	e.wantConclusion("a", 1, "success")
	e.wantConclusion("a", 2, "success")
}

func TestE2EOrgFreezeCoversRepoAddedLater(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a"}}, nil)
	e.settle("a")
	e.orgFreeze(1)
	e.r.Installation(1)
	e.wantConclusion("a", 1, "failure")

	e.addRepo(1, "late") // no webhook, no trigger
	consistently(t, func() bool { return e.gh.conclusion("o/late", 1) == "" })
	e.clk.Advance(5 * time.Minute) // periodic pass
	e.wantConclusion("late", 1, "failure")
}

func TestE2EWindowBoundaryNoTrigger(t *testing.T) {
	boundary := start0.Add(time.Hour)
	cases := map[string]model.Schedule{
		"one-off": {Kind: model.ScheduleOneOff, StartsAt: &boundary, EndsAt: ptr(start0.Add(2 * time.Hour))},
		"recurring": {
			Kind: model.ScheduleRecurring, Weekdays: model.WeekdaysOf(time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday),
			StartTime: "13:00", EndTime: "14:00",
		},
	}
	for name, sc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newE2E(t, map[int64][]string{1: {"a", "b"}}, func(c *Config) { c.Interval = -1 })
			sc.InstallationID, sc.Name, sc.Timezone, sc.Enabled = 1, "w", "UTC", true
			if _, err := e.st.CreateSchedule(context.Background(), sc); err != nil {
				t.Fatal(err)
			}
			e.settle("a", "b") // the initial pass loads the schedule and arms the timer
			eventually(t, func() bool { return e.clk.Pending() == 1 })

			e.clk.Advance(time.Hour - time.Second)
			consistently(t, func() bool { return e.gh.conclusion("o/a", 1) == "success" })
			e.clk.Advance(time.Second) // exactly the window start
			e.wantConclusion("a", 1, "failure")
			e.wantConclusion("b", 1, "failure")

			eventually(t, func() bool { return e.clk.Pending() == 1 })
			e.clk.Advance(time.Hour) // window end
			e.wantConclusion("a", 1, "success")
			e.wantConclusion("b", 1, "success")
			if name == "recurring" { // and again the next day
				eventually(t, func() bool { return e.clk.Pending() == 1 })
				e.clk.Advance(23 * time.Hour)
				e.wantConclusion("a", 1, "failure")
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestE2EMissedWebhookHeals(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a"}}, nil)
	e.settle("a")
	// Behind the Service's back: a freeze lands in the store with no trigger,
	// and a new PR opens with no webhook.
	e.orgFreeze(1)
	e.gh.addPR("o/a", 2)
	consistently(t, func() bool { return e.gh.conclusion("o/a", 1) == "success" && e.gh.conclusion("o/a", 2) == "" })
	e.clk.Advance(5 * time.Minute)
	e.wantConclusion("a", 1, "failure")
	e.wantConclusion("a", 2, "failure")
}

func TestE2ECoalescing(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a"}}, func(c *Config) { c.Interval = -1 })
	e.settle("a")
	base := e.gh.listCount("o/a")
	id := e.repoID("a")
	gate := make(chan struct{})
	e.gh.setGate(gate)
	e.r.Repo(id)
	eventually(t, func() bool { return e.gh.enteredCount() == 1 })
	for i := 0; i < 200; i++ {
		e.r.Repo(id)
		if i%20 == 0 {
			e.r.Installation(1)
			e.r.All()
		}
	}
	eventually(t, func() bool { // the run loop has consumed every trigger
		e.r.mu.Lock()
		defer e.r.mu.Unlock()
		return len(e.r.repos) == 0 && len(e.r.insts) == 0 && !e.r.all && len(e.r.wake) == 0
	})
	time.Sleep(20 * time.Millisecond) // let the loop finish expanding
	close(gate)
	eventually(t, func() bool { return e.gh.listCount("o/a") == base+2 })
	consistently(t, func() bool { return e.gh.listCount("o/a") == base+2 })
}

func TestE2ERateLimitBackoff(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a"}, 2: {"b"}}, func(c *Config) { c.Interval = -1 })
	e.settle("a", "b")
	e.gh.setInject(func(inst int64, repo string) error {
		if inst == 1 {
			return &github.HTTPError{Status: 403, Method: "GET", Body: "You have exceeded a secondary rate limit"}
		}
		return nil
	})
	e.orgFreeze(1)
	e.orgFreeze(2)
	e.r.All()
	e.wantConclusion("b", 1, "failure") // B unaffected
	eventually(t, func() bool { return e.gh.listCount("o/a") >= 2 })
	consistently(t, func() bool { return true })
	n := e.gh.listCount("o/a")
	e.clk.Advance(29 * time.Second)
	consistently(t, func() bool { return e.gh.listCount("o/a") == n && e.gh.conclusion("o/a", 1) == "success" })

	e.gh.setInject(nil)
	e.clk.Advance(time.Second) // backoff (30s base) elapses
	e.wantConclusion("a", 1, "failure")
}

func TestE2EGracefulShutdown(t *testing.T) {
	e := newE2E(t, map[int64][]string{1: {"a", "b"}}, func(c *Config) { c.Interval = -1; c.Concurrency = 1 })
	e.settle("a", "b")
	e.orgFreeze(1)
	gate := make(chan struct{})
	e.gh.setGate(gate)
	e.r.Repo(e.repoID("a"))
	eventually(t, func() bool { return e.gh.enteredCount() == 1 })
	e.r.Repo(e.repoID("b"))
	eventually(t, func() bool { return e.r.q.len() == 1 })

	e.stop()
	consistently(t, func() bool {
		select {
		case <-e.done:
			return false
		default:
			return true
		}
	})
	close(gate)
	select {
	case err := <-e.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after in-flight job finished")
	}
	if got := e.gh.conclusion("o/a", 1); got != "failure" {
		t.Fatalf("in-flight job should have completed, a = %q", got)
	}
	if got := e.gh.conclusion("o/b", 1); got != "success" {
		t.Fatalf("queued job must not start after shutdown, b = %q", got)
	}
}
