package main

import (
	"log/slog"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/reconcile"
)

// defaultReconcileInterval is how often the reconciler re-checks every repo
// to heal missed webhooks.
const defaultReconcileInterval = 5 * time.Minute

// newReconciler builds the reconciler that keeps Check Runs in line with
// freeze state. interval is the periodic full pass (negative disables it).
func newReconciler(st reconcile.Store, re reconcile.Reevaluator, logger *slog.Logger, interval time.Duration) (*reconcile.Reconciler, error) {
	return reconcile.New(reconcile.Config{
		Store:       st,
		Reevaluator: re,
		Engine:      engine.New(),
		Logger:      logger,
		Interval:    interval,
	})
}
