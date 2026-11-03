package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

var start0 = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

// fakeReeval records calls and lets tests script behaviour per repo.
type fakeReeval struct {
	mu      sync.Mutex
	calls   map[int64]int
	total   int
	running int
	maxRun  int
	instOf  func(int64) int64
	runInst map[int64]int
	maxInst map[int64]int
	order   []int64
	hook    func(ctx context.Context, repoID int64, call int) error
}

func newFakeReeval() *fakeReeval {
	return &fakeReeval{calls: map[int64]int{}, runInst: map[int64]int{}, maxInst: map[int64]int{}}
}

func (f *fakeReeval) ReevaluateRepo(ctx context.Context, repoID int64) (github.ReevaluateResult, error) {
	f.mu.Lock()
	f.calls[repoID]++
	f.total++
	n := f.calls[repoID]
	f.order = append(f.order, repoID)
	f.running++
	if f.running > f.maxRun {
		f.maxRun = f.running
	}
	var inst int64
	if f.instOf != nil {
		inst = f.instOf(repoID)
		f.runInst[inst]++
		if f.runInst[inst] > f.maxInst[inst] {
			f.maxInst[inst] = f.runInst[inst]
		}
	}
	hook := f.hook
	f.mu.Unlock()
	var err error
	if hook != nil {
		err = hook(ctx, repoID, n)
	}
	f.mu.Lock()
	f.running--
	if f.instOf != nil {
		f.runInst[inst]--
	}
	f.mu.Unlock()
	return github.ReevaluateResult{}, err
}

func (f *fakeReeval) count(id int64) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[id] }
func (f *fakeReeval) totalCalls() int    { f.mu.Lock(); defer f.mu.Unlock(); return f.total }

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

// consistently asserts cond stays true for a short real period.
func consistently(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if !cond() {
			t.Fatal("condition violated")
		}
		time.Sleep(time.Millisecond)
	}
}

type env struct {
	st  *store.SQLite
	clk *fakeClock
	ev  *fakeReeval
	r   *Reconciler
}

// newEnv seeds installations: map installation id -> repo count; repo ids are
// inst*1000+i.
func newEnv(t *testing.T, insts map[int64]int, mod func(*Config)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for id, n := range insts {
		if err := st.UpsertInstallation(ctx, model.Installation{ID: id, AccountLogin: fmt.Sprint("a", id), AccountType: "Organization"}); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			if err := st.UpsertRepo(ctx, model.Repo{ID: id*1000 + int64(i), InstallationID: id, Owner: "o", Name: fmt.Sprint("r", id, "-", i), DefaultBranch: "main"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e := &env{st: st, clk: newFakeClock(start0), ev: newFakeReeval()}
	e.ev.instOf = func(r int64) int64 { return r / 1000 }
	cfg := Config{Store: st, Reevaluator: e.ev, Clock: e.clk, Interval: -1}
	if mod != nil {
		mod(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.skipInitial = true
	e.r = r
	return e
}

func (e *env) run(t *testing.T) (cancel context.CancelFunc, done chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	finished := make(chan struct{})
	go func() { done <- e.r.Run(ctx); close(finished) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
		}
	})
	return cancel, done
}

func TestTriggerRepoReevaluatesOnce(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 2}, nil)
	e.run(t)
	e.r.Repo(1001)
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
	consistently(t, func() bool { return e.ev.totalCalls() == 1 })
}

func TestTriggerBeforeRunProcessed(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, nil)
	e.r.Repo(1001)
	e.run(t)
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
}

func TestCoalesceWhileQueued(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, nil)
	for i := 0; i < 10; i++ {
		e.r.Repo(1001)
	}
	e.run(t)
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
	consistently(t, func() bool { return e.ev.count(1001) == 1 })
}

func TestCoalesceWhileRunningGivesTwo(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	e := newEnv(t, map[int64]int{1: 1}, nil)
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		started <- struct{}{}
		if call == 1 {
			<-release
		}
		return nil
	}
	e.run(t)
	e.r.Repo(1001)
	<-started
	for i := 0; i < 10; i++ {
		e.r.Repo(1001)
	}
	// give the run loop time to push them all
	time.Sleep(20 * time.Millisecond)
	close(release)
	eventually(t, func() bool { return e.ev.count(1001) == 2 })
	consistently(t, func() bool { return e.ev.count(1001) == 2 })
}

func TestInstallationExpandsSkippingRemovedArchivedSuspended(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 4, 2: 2}, nil)
	ctx := context.Background()
	if err := e.st.MarkReposRemoved(ctx, 1, []int64{1002}, start0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertRepo(ctx, model.Repo{ID: 1003, InstallationID: 1, Owner: "o", Name: "x", Archived: true}); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	e.r.Installation(1)
	eventually(t, func() bool { return e.ev.count(1001) == 1 && e.ev.count(1004) == 1 })
	consistently(t, func() bool {
		return e.ev.count(1002) == 0 && e.ev.count(1003) == 0 && e.ev.count(2001) == 0 && e.ev.totalCalls() == 2
	})
	// suspend installation 2: All() skips it
	sus := start0
	if err := e.st.UpsertInstallation(ctx, model.Installation{ID: 2, AccountLogin: "a2", AccountType: "Organization", SuspendedAt: &sus}); err != nil {
		t.Fatal(err)
	}
	e.r.All()
	eventually(t, func() bool { return e.ev.count(1001) == 2 })
	consistently(t, func() bool { return e.ev.count(2001) == 0 && e.ev.count(2002) == 0 })
}

func TestUnknownRepoDropped(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, nil)
	e.run(t)
	e.r.Repo(99999)
	e.r.Repo(1001)
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
	consistently(t, func() bool { return e.ev.count(99999) == 0 })
}

func TestConcurrencyBounds(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, map[int64]int{1: 6, 2: 6}, func(c *Config) { c.Concurrency = 3; c.PerInstallationConcurrency = 2 })
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		<-gate
		return nil
	}
	e.run(t)
	e.r.All()
	eventually(t, func() bool { e.ev.mu.Lock(); defer e.ev.mu.Unlock(); return e.ev.running == 3 })
	consistently(t, func() bool { e.ev.mu.Lock(); defer e.ev.mu.Unlock(); return e.ev.running <= 3 })
	close(gate)
	eventually(t, func() bool { return e.ev.totalCalls() == 12 })
	e.ev.mu.Lock()
	defer e.ev.mu.Unlock()
	if e.ev.maxRun > 3 {
		t.Fatalf("maxRun=%d", e.ev.maxRun)
	}
	for i, m := range e.ev.maxInst {
		if m > 2 {
			t.Fatalf("installation %d ran %d at once", i, m)
		}
	}
}

func TestFairnessBetweenInstallations(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 30, 2: 2}, func(c *Config) { c.Concurrency = 1; c.PerInstallationConcurrency = 1 })
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		if id == 1001 { // hold the only worker until everything is queued
			eventually(t, func() bool { return e.r.q.len() == 31 })
		}
		return nil
	}
	e.run(t)
	e.r.Installation(1)
	eventually(t, func() bool { return e.ev.totalCalls() == 1 })
	e.r.Installation(2)
	eventually(t, func() bool { return e.ev.totalCalls() == 32 })
	e.ev.mu.Lock()
	defer e.ev.mu.Unlock()
	seen := 0
	for i, id := range e.ev.order {
		if id/1000 == 2 {
			seen++
			if i > 6 {
				t.Fatalf("installation 2 job served at position %d: %v", i, e.ev.order)
			}
		}
	}
	if seen != 2 {
		t.Fatal(seen)
	}
}

func rateLimitErr(status int, body string) error {
	return fmt.Errorf("wrapped: %w", &github.HTTPError{Status: status, Method: "GET", Path: "/x", Body: body})
}

func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{rateLimitErr(429, ""), true},
		{rateLimitErr(403, "You have exceeded a Secondary Rate Limit"), true},
		{rateLimitErr(403, "abuse detection"), true},
		{rateLimitErr(403, "secondary"), true},
		{rateLimitErr(403, "forbidden"), false},
		{rateLimitErr(500, "rate limit"), false},
		{errors.Join(errors.New("x"), rateLimitErr(429, "")), true},
		{errors.New("boom"), false},
	}
	for i, c := range cases {
		if got := isRateLimit(c.err); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestRateLimitBlocksOnlyThatInstallationAndBacksOff(t *testing.T) {
	var limited atomic.Bool
	limited.Store(true)
	e := newEnv(t, map[int64]int{1: 1, 2: 1}, func(c *Config) {
		c.BackoffBase = 30 * time.Second
		c.BackoffMax = 10 * time.Minute
	})
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		if id == 1001 && limited.Load() {
			return rateLimitErr(429, "")
		}
		return nil
	}
	e.run(t)
	e.r.Repo(1001)
	e.r.Repo(2001)
	eventually(t, func() bool { return e.ev.count(1001) == 1 && e.ev.count(2001) == 1 })
	consistently(t, func() bool { return e.ev.count(1001) == 1 })
	// other installation unaffected
	e.r.Repo(2001)
	eventually(t, func() bool { return e.ev.count(2001) == 2 })

	e.clk.Advance(29 * time.Second)
	consistently(t, func() bool { return e.ev.count(1001) == 1 })
	e.clk.Advance(time.Second) // 30s: first backoff elapsed
	eventually(t, func() bool { return e.ev.count(1001) == 2 })
	// second failure: backoff doubles to 60s
	consistently(t, func() bool { return e.ev.count(1001) == 2 })
	e.clk.Advance(59 * time.Second)
	consistently(t, func() bool { return e.ev.count(1001) == 2 })
	limited.Store(false)
	e.clk.Advance(time.Second)
	eventually(t, func() bool { return e.ev.count(1001) == 3 })

	// success reset the penalty: next limit is back to base 30s
	limited.Store(true)
	e.r.Repo(1001)
	eventually(t, func() bool { return e.ev.count(1001) == 4 })
	time.Sleep(10 * time.Millisecond)
	e.clk.Advance(30 * time.Second)
	eventually(t, func() bool { return e.ev.count(1001) == 5 })
}

func TestOtherErrorsRetriedThenDropped(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, func(c *Config) { c.MaxRetries = 2; c.BackoffBase = time.Second })
	e.ev.hook = func(ctx context.Context, id int64, call int) error { return errors.New("boom") }
	e.run(t)
	e.r.Repo(1001)
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
	e.clk.Advance(time.Second)
	eventually(t, func() bool { return e.ev.count(1001) == 2 })
	e.clk.Advance(2 * time.Second)
	eventually(t, func() bool { return e.ev.count(1001) == 3 })
	e.clk.Advance(time.Hour)
	consistently(t, func() bool { return e.ev.count(1001) == 3 })
}

func TestPeriodicLoop(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 2}, func(c *Config) { c.Interval = time.Minute })
	e.r.skipInitial = false
	e.run(t)
	eventually(t, func() bool { return e.ev.totalCalls() == 2 }) // initial
	for i := 2; i <= 4; i++ {
		eventually(t, func() bool { return e.clk.Pending() >= 1 })
		e.clk.Advance(time.Minute)
		n := i * 2
		eventually(t, func() bool { return e.ev.totalCalls() == n })
	}
}

func TestPeriodicDisabled(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, nil)
	e.r.skipInitial = false
	e.run(t)
	eventually(t, func() bool { return e.ev.totalCalls() == 1 })
	e.clk.Advance(24 * time.Hour)
	consistently(t, func() bool { return e.ev.totalCalls() == 1 && e.clk.Pending() == 0 })
}

func TestShutdownWaitsForInFlightAndGrace(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	e := newEnv(t, map[int64]int{1: 1}, func(c *Config) { c.ShutdownGrace = 10 * time.Second })
	var ctxErrAtFinish atomic.Value
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		close(started)
		<-release
		ctxErrAtFinish.Store(fmt.Sprint(ctx.Err()))
		return nil
	}
	cancel, done := e.run(t)
	e.r.Repo(1001)
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while job in flight")
	case <-time.After(30 * time.Millisecond):
	}
	// job context is not cancelled by ctx cancel itself
	eventually(t, func() bool { return e.clk.Pending() >= 1 })
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if v := ctxErrAtFinish.Load(); v != "<nil>" {
		t.Fatalf("job ctx err = %v", v)
	}
}

func TestShutdownGraceCancelsJob(t *testing.T) {
	started := make(chan struct{})
	e := newEnv(t, map[int64]int{1: 1}, func(c *Config) { c.ShutdownGrace = 10 * time.Second })
	e.ev.hook = func(ctx context.Context, id int64, call int) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	cancel, done := e.run(t)
	e.r.Repo(1001)
	<-started
	cancel()
	eventually(t, func() bool { return e.clk.Pending() >= 1 })
	e.clk.Advance(10 * time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after grace")
	}
	if e.ev.count(1001) != 1 {
		t.Fatal("job should not be retried on shutdown")
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error")
	}
}
