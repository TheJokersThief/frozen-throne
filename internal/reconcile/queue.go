package reconcile

import (
	"context"
	"sync"
	"time"
)

// job identifies one repository to reconcile.
type job struct {
	installationID int64
	repoID         int64
}

// queue is a fair, deduplicating queue of repositories.
//
//   - A repo is queued at most once; pushes of an already queued repo are
//     no-ops.
//   - A push for a repo that is currently running marks it dirty, and done
//     re-queues it exactly once however many pushes arrived meanwhile.
//   - pop serves installations round-robin (FIFO within an installation), so
//     an installation with many repos cannot starve another. It skips
//     installations that are blocked or already running perInstallationLimit
//     jobs. Global concurrency is the caller's number of workers.
type queue struct {
	clock Clock
	limit int

	mu      sync.Mutex
	closed  bool
	changed chan struct{} // closed and replaced on every state change
	insts   map[int64]*instState
	order   []int64 // installation ids in first-seen order
	cursor  int     // index in order to start the next scan from
	queued  map[int64]bool
	running map[int64]*runState
	nQueued int
}

type instState struct {
	pending      []int64
	running      int
	blockedUntil time.Time
}

type runState struct {
	installationID int64
	dirty          bool
}

func newQueue(clock Clock, perInstallationLimit int) *queue {
	if perInstallationLimit < 1 {
		perInstallationLimit = 1
	}
	return &queue{
		clock:   clock,
		limit:   perInstallationLimit,
		changed: make(chan struct{}),
		insts:   map[int64]*instState{},
		queued:  map[int64]bool{},
		running: map[int64]*runState{},
	}
}

// notifyLocked wakes every blocked pop to re-evaluate.
func (q *queue) notifyLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *queue) instLocked(id int64) *instState {
	s, ok := q.insts[id]
	if !ok {
		s = &instState{}
		q.insts[id] = s
		q.order = append(q.order, id)
	}
	return s
}

func (q *queue) enqueueLocked(installationID, repoID int64) {
	s := q.instLocked(installationID)
	s.pending = append(s.pending, repoID)
	q.queued[repoID] = true
	q.nQueued++
}

// push queues repoID for installationID, coalescing with queued or running work.
func (q *queue) push(installationID, repoID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.queued[repoID] {
		return
	}
	if r, ok := q.running[repoID]; ok {
		r.dirty = true
		return
	}
	q.enqueueLocked(installationID, repoID)
	q.notifyLocked()
}

// pop blocks until a job is eligible. It returns false if ctx is done or the
// queue is closed.
func (q *queue) pop(ctx context.Context) (job, bool) {
	for {
		q.mu.Lock()
		if q.closed || ctx.Err() != nil {
			q.mu.Unlock()
			return job{}, false
		}
		if j, ok := q.takeLocked(); ok {
			q.mu.Unlock()
			return j, true
		}
		ch := q.changed
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return job{}, false
		}
	}
}

func (q *queue) takeLocked() (job, bool) {
	now := q.clock.Now()
	n := len(q.order)
	for i := 0; i < n; i++ {
		idx := (q.cursor + i) % n
		id := q.order[idx]
		s := q.insts[id]
		if len(s.pending) == 0 || s.running >= q.limit || s.blockedUntil.After(now) {
			continue
		}
		repoID := s.pending[0]
		s.pending = s.pending[1:]
		s.running++
		delete(q.queued, repoID)
		q.nQueued--
		q.running[repoID] = &runState{installationID: id}
		q.cursor = (idx + 1) % n
		return job{installationID: id, repoID: repoID}, true
	}
	return job{}, false
}

// done marks j finished, re-queueing it once if it was pushed while running.
func (q *queue) done(j job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.running[j.repoID]
	if !ok {
		return
	}
	delete(q.running, j.repoID)
	q.insts[r.installationID].running--
	if r.dirty && !q.closed {
		q.enqueueLocked(r.installationID, j.repoID)
	}
	q.notifyLocked()
}

// blockUntil stops serving installationID until the clock reaches t. Callers
// must arrange a wake at t (typically Clock.AfterFunc).
func (q *queue) blockUntil(installationID int64, t time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.instLocked(installationID).blockedUntil = t
	q.notifyLocked()
}

// wake makes blocked pops re-evaluate eligibility (e.g. after the clock moved).
func (q *queue) wake() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.notifyLocked()
}

// len returns the number of queued (not running) repos.
func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.nQueued
}

// close releases all blocked pops; further pushes are ignored.
func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.notifyLocked()
}
