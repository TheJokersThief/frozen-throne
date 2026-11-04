package reconcile

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestFakeClockFiresInTimeOrder(t *testing.T) {
	c := newFakeClock(t0)
	var got []int
	c.AfterFunc(3*time.Second, func() { got = append(got, 3) })
	c.AfterFunc(1*time.Second, func() { got = append(got, 1) })
	c.AfterFunc(2*time.Second, func() { got = append(got, 2) })
	c.Advance(1500 * time.Millisecond)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("got %v", got)
	}
	c.Advance(10 * time.Second)
	if len(got) != 3 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("got %v", got)
	}
	if !c.Now().Equal(t0.Add(11500 * time.Millisecond)) {
		t.Fatalf("now = %v", c.Now())
	}
}

func TestFakeClockNowDuringCallbackIsDueTime(t *testing.T) {
	c := newFakeClock(t0)
	var at time.Time
	c.AfterFunc(time.Minute, func() { at = c.Now() })
	c.Advance(time.Hour)
	if !at.Equal(t0.Add(time.Minute)) {
		t.Fatalf("callback saw %v", at)
	}
}

func TestFakeClockStopAndPending(t *testing.T) {
	c := newFakeClock(t0)
	fired := false
	tm := c.AfterFunc(time.Second, func() { fired = true })
	if c.Pending() != 1 {
		t.Fatal("want 1 pending")
	}
	if !tm.Stop() {
		t.Fatal("first Stop should report true")
	}
	if tm.Stop() {
		t.Fatal("second Stop should report false")
	}
	c.Advance(time.Minute)
	if fired || c.Pending() != 0 {
		t.Fatalf("fired=%v pending=%d", fired, c.Pending())
	}
}

func TestFakeClockCallbackMayScheduleMore(t *testing.T) {
	c := newFakeClock(t0)
	n := 0
	var tick func()
	tick = func() {
		n++
		if n < 3 {
			c.AfterFunc(time.Second, tick)
		}
	}
	c.AfterFunc(time.Second, tick)
	c.Advance(10 * time.Second)
	if n != 3 {
		t.Fatalf("n = %d", n)
	}
}

func TestFakeClockSet(t *testing.T) {
	c := newFakeClock(t0)
	fired := false
	c.AfterFunc(time.Hour, func() { fired = true })
	c.Set(t0.Add(2 * time.Hour))
	if !fired {
		t.Fatal("not fired")
	}
}

func TestRealClock(t *testing.T) {
	var c Clock = RealClock{}
	before := time.Now()
	if c.Now().Before(before) {
		t.Fatal("Now went backwards")
	}
	done := make(chan struct{})
	c.AfterFunc(time.Millisecond, func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AfterFunc did not fire")
	}
	tm := c.AfterFunc(time.Hour, func() {})
	if !tm.Stop() {
		t.Fatal("Stop of pending timer should be true")
	}
}
