package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// fakeEngine freezes on org/repo freezes and one-off windows.
type fakeEngine struct{}

func (fakeEngine) Evaluate(in engine.Input) (engine.Decision, error) {
	var d engine.Decision
	if in.OrgFreeze != nil {
		d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceOrgFreeze, FreezeID: in.OrgFreeze.ID, Text: in.OrgFreeze.Reason, Since: in.OrgFreeze.CreatedAt})
	}
	if in.RepoFreeze != nil {
		d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceRepoFreeze, FreezeID: in.RepoFreeze.ID, Text: in.RepoFreeze.Reason, Since: in.RepoFreeze.CreatedAt})
	}
	for _, s := range in.Schedules {
		if !s.Enabled || s.Kind != model.ScheduleOneOff {
			continue
		}
		switch {
		case !in.At.Before(*s.StartsAt) && in.At.Before(*s.EndsAt):
			e := *s.EndsAt
			d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceSchedule, ScheduleID: s.ID, Text: s.Name, Since: *s.StartsAt, Until: &e})
			d.NextChange = &e
		case in.At.Before(*s.StartsAt):
			st := *s.StartsAt
			d.NextChange = &st
		}
	}
	d.Frozen = len(d.Reasons) > 0
	d.Blocked = d.Frozen
	return d, nil
}

// fakePerms grants roles per login.
type fakePerms struct {
	mu    sync.Mutex
	org   map[string]Role           // login -> org role
	repo  map[string]map[int64]Role // login -> repo id -> role
	calls int
	err   error
}

func (f *fakePerms) RepoRole(_ context.Context, a Actor, r model.Repo) (Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.repo[a.User.Login][r.ID], f.err
}

func (f *fakePerms) OrgRole(_ context.Context, a Actor, _ model.Installation) (Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.org[a.User.Login], f.err
}

type fakeOAuth struct{ token string }

func (fakeOAuth) AuthorizeURL(state string) string {
	return "https://github.test/authorize?state=" + state
}
func (f fakeOAuth) Exchange(_ context.Context, code string) (string, error) {
	if code != "good" {
		return "", ErrBadCode
	}
	return f.token, nil
}
func (fakeOAuth) User(context.Context, string) (model.User, error) {
	return model.User{ID: 1, Login: "alice", Name: "Alice"}, nil
}

type recorder struct {
	mu  sync.Mutex
	evs []FreezeChange
}

func (r *recorder) FreezeStateChanged(_ context.Context, e FreezeChange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *recorder) take() []FreezeChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.evs
	r.evs = nil
	return out
}

type env struct {
	t     *testing.T
	srv   *Server
	h     http.Handler
	st    store.Store
	perms *fakePerms
	rec   *recorder
	now   time.Time
	web   model.Repo // acme/web, id 10
	api   model.Repo // acme/api, id 11
}

// Users: alice=org owner, bob=maintainer of web only, carol=member of web,
// dave=no access. Ids 1..4.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertInstallation(ctx, model.Installation{ID: 1, AccountLogin: "acme", AccountID: 100, AccountType: "Organization"}))
	web := model.Repo{ID: 10, InstallationID: 1, Owner: "acme", Name: "web", DefaultBranch: "main"}
	api := model.Repo{ID: 11, InstallationID: 1, Owner: "acme", Name: "api", DefaultBranch: "main", Private: true}
	must(st.UpsertRepo(ctx, web))
	must(st.UpsertRepo(ctx, api))
	for i, l := range []string{"alice", "bob", "carol", "dave"} {
		must(st.UpsertUser(ctx, model.User{ID: int64(i + 1), Login: l}))
	}
	perms := &fakePerms{
		org: map[string]Role{"alice": RoleOwner},
		repo: map[string]map[int64]Role{
			"bob":   {10: RoleMaintainer},
			"carol": {10: RoleMember},
		},
	}
	rec := &recorder{}
	srv, err := New(Config{
		SessionSecret: bytes.Repeat([]byte("k"), 32), PermissionCacheTTL: -1,
		Now: func() time.Time { return now },
	}, Deps{Store: st, Engine: fakeEngine{}, OAuth: fakeOAuth{token: "gh-token"}, Permissions: perms, Notifier: rec})
	must(err)
	return &env{t: t, srv: srv, h: srv.Handler(), st: st, perms: perms, rec: rec, now: now, web: web, api: api}
}

// login creates a session for the named user (ids as in newEnv) and returns
// its cookie.
func (e *env) login(user string) *http.Cookie {
	e.t.Helper()
	ids := map[string]int64{"alice": 1, "bob": 2, "carol": 3, "dave": 4}
	tok, err := newToken()
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.CreateSession(context.Background(), model.Session{
		ID: sessionID(tok), UserID: ids[user], GitHubToken: e.srv.sess.seal("tok-" + user),
		CreatedAt: e.now, ExpiresAt: e.now.Add(time.Hour), LastSeenAt: e.now,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: e.srv.sess.cookieValue(tok)}
}

type resp struct {
	*httptest.ResponseRecorder
}

func (r resp) decode(v any) {
	json.Unmarshal(r.Body.Bytes(), v)
}

// do sends a request as the given cookie (nil = anonymous). body may be nil,
// a string (raw) or any JSON-able value. Mutations get the CSRF header.
func (e *env) do(c *http.Cookie, method, path string, body any) resp {
	e.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, Prefix+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, csrfValue)
	}
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return resp{w}
}

func (e *env) expect(r resp, status int) resp {
	e.t.Helper()
	if r.Code != status {
		e.t.Fatalf("status = %d, want %d; body: %s", r.Code, status, r.Body.String())
	}
	return r
}

func ptr[T any](v T) *T { return &v }
