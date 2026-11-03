package reconcile

import (
	"context"
	"testing"
	"time"
)

type popResult struct {
	j  job
	ok bool
}

func popAsync(ctx context.Context, q *queue) <-chan popResult {
	ch := make(chan popResult, 1)
	go func() {
		j, ok := q.pop(ctx)
		ch <- popResult{j, ok}
	}()
	return ch
}

func mustPop(t *testing.T, q *queue) job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, ok := q.pop(ctx)
	if !ok {
		t.Fatal("pop returned !ok")
	}
	return j
}

func expectBlocked(t *testing.T, ch <-chan popResult) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("pop should block, got %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func expectResult(t *testing.T, ch <-chan popResult) popResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("pop did not return")
		return popResult{}
	}
}

func TestQueueDedupesQueuedPushes(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	q.push(1, 100)
	q.push(1, 100)
	q.push(1, 100)
	if q.len() != 1 {
		t.Fatalf("len = %d", q.len())
	}
	j := mustPop(t, q)
	if j != (job{1, 100}) {
		t.Fatalf("job = %+v", j)
	}
	if q.len() != 0 {
		t.Fatalf("len = %d after pop", q.len())
	}
}

func TestQueueFIFOWithinInstallation(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	for _, r := range []int64{3, 1, 2} {
		q.push(1, r)
	}
	for _, want := range []int64{3, 1, 2} {
		if got := mustPop(t, q).repoID; got != want {
			t.Fatalf("got %d want %d", got, want)
		}
	}
}

func TestQueuePushWhileRunningCoalescesToOneRerun(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	q.push(1, 100)
	j := mustPop(t, q)
	for i := 0; i < 5; i++ {
		q.push(1, 100)
	}
	if q.len() != 0 {
		t.Fatalf("running repo must not be queued, len = %d", q.len())
	}
	q.done(j)
	if q.len() != 1 {
		t.Fatalf("want exactly one rerun queued, len = %d", q.len())
	}
	j2 := mustPop(t, q)
	if j2 != j {
		t.Fatalf("rerun = %+v", j2)
	}
	q.done(j2)
	if q.len() != 0 {
		t.Fatalf("no further reruns expected, len = %d", q.len())
	}
}

func TestQueueDoneWithoutPushLeavesNothing(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	q.push(1, 100)
	q.done(mustPop(t, q))
	if q.len() != 0 {
		t.Fatalf("len = %d", q.len())
	}
	q.push(1, 100) // can be queued again after done
	if q.len() != 1 {
		t.Fatalf("len = %d", q.len())
	}
}

func TestQueueRoundRobinAcrossInstallations(t *testing.T) {
	q := newQueue(newFakeClock(t0), 100)
	for r := int64(1); r <= 50; r++ {
		q.push(1, r)
	}
	q.push(2, 1001)
	q.push(2, 1002)

	var order []int64
	for i := 0; i < 4; i++ {
		order = append(order, mustPop(t, q).installationID)
	}
	// Installation 2 must be served within the first two pops and again
	// within the next two, rather than waiting behind all 50 of installation 1.
	if !(contains(order[:2], 2) && contains(order[2:], 2)) {
		t.Fatalf("installation 2 starved: %v", order)
	}
}

func contains(s []int64, v int64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestQueuePerInstallationLimit(t *testing.T) {
	q := newQueue(newFakeClock(t0), 2)
	for r := int64(1); r <= 4; r++ {
		q.push(1, r)
	}
	a := mustPop(t, q)
	b := mustPop(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := popAsync(ctx, q)
	expectBlocked(t, ch)

	q.done(a)
	r := expectResult(t, ch)
	if !r.ok || r.j.installationID != 1 {
		t.Fatalf("result = %+v", r)
	}
	_ = b
}

func TestQueueLimitDoesNotBlockOtherInstallation(t *testing.T) {
	q := newQueue(newFakeClock(t0), 1)
	q.push(1, 1)
	q.push(1, 2)
	q.push(2, 3)
	first := mustPop(t, q)
	second := mustPop(t, q)
	if first.installationID == second.installationID {
		t.Fatalf("both from installation %d despite limit 1", first.installationID)
	}
}

func TestQueueBlockUntilSkipsInstallation(t *testing.T) {
	clk := newFakeClock(t0)
	q := newQueue(clk, 10)
	q.blockUntil(1, t0.Add(time.Minute))
	q.push(1, 1)
	q.push(2, 2)

	if j := mustPop(t, q); j.installationID != 2 {
		t.Fatalf("blocked installation served: %+v", j)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := popAsync(ctx, q)
	expectBlocked(t, ch)

	clk.Advance(time.Minute - time.Second)
	q.wake()
	expectBlocked(t, ch)

	clk.Advance(time.Second)
	q.wake()
	r := expectResult(t, ch)
	if !r.ok || r.j != (job{1, 1}) {
		t.Fatalf("result = %+v", r)
	}
}

func TestQueueBlockUntilInPastIsNoBlock(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	q.blockUntil(1, t0.Add(-time.Hour))
	q.push(1, 1)
	mustPop(t, q)
}

func TestQueuePopBlocksThenUnblocksOnPush(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := popAsync(ctx, q)
	expectBlocked(t, ch)
	q.push(7, 70)
	r := expectResult(t, ch)
	if !r.ok || r.j != (job{7, 70}) {
		t.Fatalf("result = %+v", r)
	}
}

func TestQueuePopReturnsOnContextCancel(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	ctx, cancel := context.WithCancel(context.Background())
	ch := popAsync(ctx, q)
	expectBlocked(t, ch)
	cancel()
	if r := expectResult(t, ch); r.ok {
		t.Fatalf("want !ok, got %+v", r)
	}
}

func TestQueuePopReturnsOnClose(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	ch1 := popAsync(context.Background(), q)
	ch2 := popAsync(context.Background(), q)
	expectBlocked(t, ch1)
	q.close()
	if r := expectResult(t, ch1); r.ok {
		t.Fatalf("want !ok, got %+v", r)
	}
	if r := expectResult(t, ch2); r.ok {
		t.Fatalf("want !ok, got %+v", r)
	}
	// pop after close does not block, push after close is a no-op.
	q.push(1, 1)
	if _, ok := q.pop(context.Background()); ok {
		t.Fatal("pop after close should be !ok")
	}
	q.close() // idempotent
}

func TestQueuePopWithAlreadyCancelledContext(t *testing.T) {
	q := newQueue(newFakeClock(t0), 10)
	q.push(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := q.pop(ctx); ok {
		t.Fatal("cancelled ctx should yield !ok")
	}
}
