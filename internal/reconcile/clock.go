package reconcile

import "time"

// Clock abstracts time so schedules and backoff are testable.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine (real clock) or synchronously
	// during Advance (fake clock) after d. f must not block.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc.
type Timer interface {
	// Stop prevents the timer firing and reports whether it was still pending.
	Stop() bool
}

// RealClock is the wall-clock implementation of Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
