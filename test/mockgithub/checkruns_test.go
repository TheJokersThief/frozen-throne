package mockgithub_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func TestCreateUpdateAndInspectCheckRun(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	app := h.app(t)

	id, err := app.CreateCheckRun(ctx, 7, "acme", "web", github.CheckRun{HeadSHA: "abc123", Conclusion: "failure", Title: "Frozen", Summary: "Freeze active"})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("want non-zero id")
	}

	runs, err := h.ctl.CheckRuns(ctx, "acme", "web", "abc123", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	r := runs[0]
	if r.ID != id || r.Name != "frozen-throne" || r.Status != "completed" || r.Conclusion != "failure" ||
		r.Output.Title != "Frozen" || r.Output.Summary != "Freeze active" || r.HeadSHA != "abc123" {
		t.Fatalf("unexpected run %+v", r)
	}
	if _, err := time.Parse(time.RFC3339, r.CompletedAt); err != nil {
		t.Fatalf("completed_at: %v", err)
	}

	if err := app.UpdateCheckRun(ctx, 7, "acme", "web", id, github.CheckRun{Conclusion: "success", Title: "Open", Summary: "Thawed"}); err != nil {
		t.Fatal(err)
	}
	runs, _ = h.ctl.CheckRuns(ctx, "acme", "web", "abc123", "frozen-throne")
	if len(runs) != 1 || runs[0].Conclusion != "success" || runs[0].Output.Title != "Open" || runs[0].HeadSHA != "abc123" {
		t.Fatalf("after update: %+v", runs)
	}

	if runs, _ = h.ctl.CheckRuns(ctx, "acme", "web", "abc123", "other"); len(runs) != 0 {
		t.Fatalf("name filter: %+v", runs)
	}
	if runs, _ = h.ctl.CheckRuns(ctx, "acme", "web", "zzz", ""); len(runs) != 0 {
		t.Fatalf("sha filter: %+v", runs)
	}
}

func TestCheckRunErrors(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	app := h.app(t)

	// Repo owned by another installation is invisible.
	_, err := app.CreateCheckRun(ctx, 7, "acme", "missing", github.CheckRun{HeadSHA: "x", Conclusion: "success"})
	var he *github.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("missing repo: want 404, got %v", err)
	}
	// Updating a run that does not exist is 404.
	err = app.UpdateCheckRun(ctx, 7, "acme", "web", 99999, github.CheckRun{Conclusion: "success"})
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("missing run: want 404, got %v", err)
	}
	// Missing head_sha is a validation error.
	tok := installToken(t, h, 7)
	code, _ := do(t, "POST", h.srv.URL+"/repos/acme/web/check-runs", tok, map[string]any{"name": "frozen-throne"})
	if code != 422 {
		t.Fatalf("no head_sha: want 422, got %d", code)
	}
	code, _ = do(t, "POST", h.srv.URL+"/repos/acme/web/check-runs", "ghs_bad", map[string]any{"name": "n", "head_sha": "x"})
	if code != 401 {
		t.Fatalf("bad token: want 401, got %d", code)
	}
}

func TestWaitForCheckRun(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	app := h.app(t)

	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = app.CreateCheckRun(context.Background(), 7, "acme", "web", github.CheckRun{HeadSHA: "late", Conclusion: "success"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := h.ctl.WaitForCheckRun(ctx, "acme", "web", "late", nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Conclusion != "success" {
		t.Fatalf("got %+v", run)
	}

	short, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := h.ctl.WaitForCheckRun(short, "acme", "web", "never", nil); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestNewRejectsNilPublicKey(t *testing.T) {
	srv, err := mockgithub.New(mockgithub.Config{AppID: appID})
	if err == nil || srv != nil {
		t.Fatalf("want error and nil server, got %v, %v", srv, err)
	}
}

func TestCheckRunWithConclusionButNoStatusIsCompleted(t *testing.T) {
	h := newHarness(t)
	seedRepo(t, h)
	ctx := context.Background()
	tok := installToken(t, h, 7)

	code, body := do(t, http.MethodPost, h.srv.URL+"/repos/acme/web/check-runs", tok,
		map[string]any{"name": "x", "head_sha": "feed", "conclusion": "success"})
	if code != http.StatusCreated {
		t.Fatalf("status %d: %s", code, body)
	}
	runs, err := h.ctl.CheckRuns(ctx, "acme", "web", "feed", "x")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %+v, err %v", runs, err)
	}
	if runs[0].Status != "completed" {
		t.Fatalf("status = %q, want completed", runs[0].Status)
	}
	if _, err := time.Parse(time.RFC3339, runs[0].CompletedAt); err != nil {
		t.Fatalf("completed_at %q: %v", runs[0].CompletedAt, err)
	}
}
