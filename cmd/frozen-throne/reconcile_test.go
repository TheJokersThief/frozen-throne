package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type noopReeval struct{}

func (noopReeval) ReevaluateRepo(context.Context, int64) (github.ReevaluateResult, error) {
	return github.ReevaluateResult{}, nil
}

func TestNewReconciler(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := newReconciler(st, noopReeval{}, log, time.Minute)
	if err != nil || r == nil {
		t.Fatalf("newReconciler = %v, %v", r, err)
	}
	if _, err := newReconciler(nil, noopReeval{}, log, time.Minute); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestReconcileIntervalFlag(t *testing.T) {
	if d := defaultReconcileInterval; d != 5*time.Minute {
		t.Fatalf("default = %v", d)
	}
}
