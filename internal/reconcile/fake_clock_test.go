package reconcile

import (
	"sort"
	"sync"
	"time"
)

// fakeClock is a deterministic Clock for tests. Time only moves via Advance
// or Set; due AfterFunc callbacks fire synchronously in time order with the
// lock released. Callbacks must not block.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	when    time.Time
	seq     int
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &fakeTimer{c: c, when: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

// Advance moves time forward by d, firing due timers in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.advanceLocked(c.now.Add(d))
}

// Set moves time to t (which must not be earlier than now), firing due timers.
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.advanceLocked(t)
}

// advanceLocked is entered with c.mu held and releases it.
func (c *fakeClock) advanceLocked(target time.Time) {
	for {
		var due []*fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.when.After(target) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			break
		}
		sort.Slice(due, func(i, j int) bool {
			if !due[i].when.Equal(due[j].when) {
				return due[i].when.Before(due[j].when)
			}
			return due[i].seq < due[j].seq
		})
		t := due[0]
		t.fired = true
		if t.when.After(c.now) {
			c.now = t.when
		}
		c.mu.Unlock()
		t.f()
		c.mu.Lock()
	}
	if target.After(c.now) {
		c.now = target
	}
	live := c.timers[:0]
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			live = append(live, t)
		}
	}
	c.timers = live
	c.mu.Unlock()
}

// Pending returns the number of armed, unfired timers.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}
