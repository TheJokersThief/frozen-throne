// Package reconcile keeps GitHub check runs converged with the freeze engine.
//
// It owns a fair, deduplicating work queue of repositories, a clock
// abstraction so time-driven behaviour is testable, and the reconciler that
// drives github.Service.ReevaluateRepo from triggers, periodic sweeps and
// freeze window boundaries.
package reconcile
