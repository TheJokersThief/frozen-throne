package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// Trigger asks the reconciler to re-evaluate repositories. Methods only
// record intent: they never block, never fail and are safe to call from any
// goroutine, including before Run starts. internal/api calls them after
// freeze, thaw, schedule and bypass changes. Repeated triggers for the same
// repo coalesce.
type Trigger interface {
	// Repo re-evaluates one repository.
	Repo(repoID int64)
	// Installation re-evaluates every active repo of an installation.
	Installation(installationID int64)
	// All re-evaluates every active repo of every active installation.
	All()
}

// Reevaluator brings a repo's Check Runs in line with the freeze state.
// *github.Service implements it.
type Reevaluator interface {
	ReevaluateRepo(ctx context.Context, repoID int64) (github.ReevaluateResult, error)
}

// Store is the persistence the reconciler needs.
type Store interface {
	store.Installations
	store.Repos
	store.Freezes
	store.Schedules
}

// Config configures a Reconciler. Zero values select the documented defaults.
type Config struct {
	Store       Store
	Reevaluator Reevaluator
	Engine      engine.Evaluator
	Clock       Clock        // default RealClock
	Logger      *slog.Logger // default slog.Default()

	Interval                   time.Duration // periodic full pass; default 5m, <0 disables
	Concurrency                int           // default 4
	PerInstallationConcurrency int           // default 2
	BackoffBase                time.Duration // default 30s
	BackoffMax                 time.Duration // default 10m
	MaxRetries                 int           // retries for non-rate-limit errors; default 3
	ShutdownGrace              time.Duration // default 10s
}

// Reconciler turns triggers into bounded, fair, deduplicated calls to
// Reevaluator.ReevaluateRepo.
type Reconciler struct {
	cfg Config
	q   *queue

	wake chan struct{}

	mu       sync.Mutex
	repos    map[int64]struct{}
	insts    map[int64]struct{}
	all      bool
	penalty  map[int64]int       // installation -> consecutive rate limits
	blocked  map[int64]time.Time // installation -> backoff expiry
	attempts map[int64]int       // repo -> consecutive failed attempts
	periodic Timer
	stopped  bool

	skipInitial bool // tests: do not enqueue All() when Run starts
}

var _ Trigger = (*Reconciler)(nil)

// New validates cfg and applies defaults.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Store == nil {
		return nil, errors.New("reconcile: Store is required")
	}
	if cfg.Reevaluator == nil {
		return nil, errors.New("reconcile: Reevaluator is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.PerInstallationConcurrency <= 0 {
		cfg.PerInstallationConcurrency = 2
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 30 * time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 10 * time.Minute
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 10 * time.Second
	}
	return &Reconciler{
		cfg:      cfg,
		q:        newQueue(cfg.Clock, cfg.PerInstallationConcurrency),
		wake:     make(chan struct{}, 1),
		repos:    map[int64]struct{}{},
		insts:    map[int64]struct{}{},
		penalty:  map[int64]int{},
		blocked:  map[int64]time.Time{},
		attempts: map[int64]int{},
	}, nil
}

// Repo implements Trigger.
func (r *Reconciler) Repo(repoID int64) {
	r.mu.Lock()
	r.repos[repoID] = struct{}{}
	r.mu.Unlock()
	r.signal()
}

// Installation implements Trigger.
func (r *Reconciler) Installation(installationID int64) {
	r.mu.Lock()
	r.insts[installationID] = struct{}{}
	r.mu.Unlock()
	r.signal()
}

// All implements Trigger.
func (r *Reconciler) All() {
	r.mu.Lock()
	r.all = true
	r.mu.Unlock()
	r.signal()
}

func (r *Reconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run processes triggers until ctx is cancelled. In-flight jobs then finish on
// a context that is cancelled only after ShutdownGrace; no new jobs start. It
// returns nil on clean shutdown.
func (r *Reconciler) Run(ctx context.Context) error {
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()

	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx, jobCtx)
		}()
	}
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		r.loop(ctx)
	}()

	if !r.skipInitial {
		r.All() // initial full pass
	}
	r.armPeriodic()

	<-ctx.Done()
	grace := r.cfg.Clock.AfterFunc(r.cfg.ShutdownGrace, cancelJobs)
	defer grace.Stop()
	r.mu.Lock()
	r.stopped = true
	if r.periodic != nil {
		r.periodic.Stop()
	}
	r.mu.Unlock()
	r.q.close()
	<-loopDone
	wg.Wait()
	return nil
}

func (r *Reconciler) armPeriodic() {
	if r.cfg.Interval < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.periodic = r.cfg.Clock.AfterFunc(r.cfg.Interval, func() {
		r.All()
		r.armPeriodic()
	})
}

// loop expands recorded intent into queued repos.
func (r *Reconciler) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		r.mu.Lock()
		repos, insts, all := r.repos, r.insts, r.all
		r.repos, r.insts, r.all = map[int64]struct{}{}, map[int64]struct{}{}, false
		r.mu.Unlock()
		r.expand(ctx, repos, insts, all)
	}
}

func (r *Reconciler) expand(ctx context.Context, repos, insts map[int64]struct{}, all bool) {
	log := r.cfg.Logger
	if all {
		list, err := r.cfg.Store.ListInstallations(ctx)
		if err != nil {
			log.Error("reconcile: list installations", "err", err)
			return
		}
		for _, in := range list {
			r.expandInstallation(ctx, in)
		}
		return // covers every repo and installation trigger
	}
	for id := range insts {
		in, err := r.cfg.Store.GetInstallation(ctx, id)
		if err != nil {
			if !errors.Is(err, model.ErrNotFound) {
				log.Error("reconcile: get installation", "installation", id, "err", err)
			}
			continue
		}
		r.expandInstallation(ctx, in)
	}
	for id := range repos {
		repo, err := r.cfg.Store.GetRepo(ctx, id)
		if err != nil {
			if !errors.Is(err, model.ErrNotFound) {
				log.Error("reconcile: get repo", "repo", id, "err", err)
			}
			continue
		}
		if repo.RemovedAt != nil || repo.Archived {
			continue
		}
		r.q.push(repo.InstallationID, repo.ID)
	}
}

func (r *Reconciler) expandInstallation(ctx context.Context, in model.Installation) {
	if in.SuspendedAt != nil {
		return
	}
	repos, err := r.cfg.Store.ListRepos(ctx, store.RepoFilter{InstallationID: in.ID})
	if err != nil {
		r.cfg.Logger.Error("reconcile: list repos", "installation", in.ID, "err", err)
		return
	}
	for _, repo := range repos {
		if repo.RemovedAt != nil || repo.Archived {
			continue
		}
		r.q.push(in.ID, repo.ID)
	}
}

func (r *Reconciler) worker(ctx, jobCtx context.Context) {
	for {
		j, ok := r.q.pop(ctx)
		if !ok {
			return
		}
		r.process(ctx, jobCtx, j)
	}
}

func (r *Reconciler) process(ctx, jobCtx context.Context, j job) {
	defer r.q.done(j)
	_, err := r.cfg.Reevaluator.ReevaluateRepo(jobCtx, j.repoID)
	switch {
	case err == nil:
		r.mu.Lock()
		delete(r.attempts, j.repoID)
		delete(r.penalty, j.installationID)
		delete(r.blocked, j.installationID)
		r.mu.Unlock()
	case errors.Is(err, model.ErrNotFound):
		r.mu.Lock()
		delete(r.attempts, j.repoID)
		r.mu.Unlock()
	case ctx.Err() != nil:
		// shutting down: do not retry
	case isRateLimit(err):
		r.rateLimited(j, err)
	default:
		r.retry(j, err)
	}
}

func (r *Reconciler) backoff(n int) time.Duration {
	d := r.cfg.BackoffBase
	for i := 1; i < n && d < r.cfg.BackoffMax; i++ {
		d *= 2
	}
	if d > r.cfg.BackoffMax {
		d = r.cfg.BackoffMax
	}
	return d
}

func (r *Reconciler) rateLimited(j job, err error) {
	now := r.cfg.Clock.Now()
	r.mu.Lock()
	var until time.Time
	if b := r.blocked[j.installationID]; b.After(now) {
		until = b // another job already extended the backoff for this window
	} else {
		r.penalty[j.installationID]++
		d := r.backoff(r.penalty[j.installationID])
		until = now.Add(d)
		r.blocked[j.installationID] = until
		r.cfg.Logger.Warn("reconcile: rate limited", "installation", j.installationID, "backoff", d, "err", err)
	}
	r.mu.Unlock()
	r.q.blockUntil(j.installationID, until)
	r.q.push(j.installationID, j.repoID) // marked dirty; requeued by done
	r.cfg.Clock.AfterFunc(until.Sub(now), r.q.wake)
}

func (r *Reconciler) retry(j job, err error) {
	r.mu.Lock()
	r.attempts[j.repoID]++
	n := r.attempts[j.repoID]
	if n > r.cfg.MaxRetries {
		delete(r.attempts, j.repoID)
		r.mu.Unlock()
		r.cfg.Logger.Error("reconcile: giving up on repo", "repo", j.repoID, "err", err)
		return
	}
	r.mu.Unlock()
	d := r.backoff(n)
	r.cfg.Logger.Warn("reconcile: reevaluate failed, will retry", "repo", j.repoID, "attempt", n, "in", d, "err", err)
	r.cfg.Clock.AfterFunc(d, func() { r.q.push(j.installationID, j.repoID) })
}

// isRateLimit reports whether err is a GitHub primary or secondary rate limit.
func isRateLimit(err error) bool {
	var he *github.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	if he.Status == 429 {
		return true
	}
	if he.Status == 403 {
		b := strings.ToLower(he.Body)
		return strings.Contains(b, "rate limit") || strings.Contains(b, "abuse") || strings.Contains(b, "secondary")
	}
	return false
}
