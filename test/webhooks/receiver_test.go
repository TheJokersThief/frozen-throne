package webhooks_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	ghsvc "github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

const secret = "whsec_test"

// recordingAPI is a fake github.API that records calls.
type recordingAPI struct {
	mu      sync.Mutex
	Creates []ghsvc.CheckRun
	Updates []ghsvc.CheckRun
	Lists   int
	Pulls   []ghsvc.PullSummary
	nextID  int64
}

func (a *recordingAPI) CreateCheckRun(_ context.Context, _ int64, _, _ string, c ghsvc.CheckRun) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	a.Creates = append(a.Creates, c)
	return a.nextID, nil
}

func (a *recordingAPI) UpdateCheckRun(_ context.Context, _ int64, _, _ string, _ int64, c ghsvc.CheckRun) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Updates = append(a.Updates, c)
	return nil
}

func (a *recordingAPI) ListOpenPullRequests(context.Context, int64, string, string) ([]ghsvc.PullSummary, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Lists++
	return a.Pulls, nil
}

func (a *recordingAPI) creates() []ghsvc.CheckRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ghsvc.CheckRun(nil), a.Creates...)
}

// receiver is the real webhook handler over an in-memory store.
type receiver struct {
	URL   string
	API   *recordingAPI
	Store *store.SQLite
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := &recordingAPI{}
	svc, err := ghsvc.NewService(ghsvc.ServiceConfig{Store: st, Engine: engine.New(), API: api, WebhookSecret: []byte(secret)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc)
	t.Cleanup(srv.Close)
	return &receiver{URL: srv.URL, API: api, Store: st}
}
