package github

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

const secret = "whsec"

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeEngine freezes when an org/repo freeze or any schedule is present, and
// honours the bypass, mirroring the documented engine semantics.
type fakeEngine struct{}

func (fakeEngine) Evaluate(in engine.Input) (engine.Decision, error) {
	var d engine.Decision
	if in.OrgFreeze != nil {
		d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceOrgFreeze, Text: in.OrgFreeze.Reason})
	}
	if in.RepoFreeze != nil {
		d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceRepoFreeze, Text: in.RepoFreeze.Reason})
	}
	for _, s := range in.Schedules {
		d.Reasons = append(d.Reasons, engine.Reason{Kind: engine.SourceSchedule, Text: s.Name})
	}
	d.Frozen = len(d.Reasons) > 0
	if d.Frozen && in.PR > 0 && in.Bypass != nil && in.Bypass.PRNumber == in.PR {
		d.Bypassed, d.BypassID = true, in.Bypass.ID
	}
	d.Blocked = d.Frozen && !d.Bypassed
	return d, nil
}

type call struct {
	Method, Path string
	Body         map[string]any
}

// fakeGH is a minimal GitHub: token endpoint, check runs, pulls.
type fakeGH struct {
	*httptest.Server
	mu        sync.Mutex
	calls     []call
	tokens    int
	nextID    int64
	pulls     []map[string]any
	missingCR map[int64]bool // check run IDs that 404 on PATCH
}

func newFakeGH(t *testing.T, key *rsa.PublicKey) *fakeGH {
	g := &fakeGH{nextID: 100, missingCR: map[int64]bool{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		path := r.URL.Path
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/app/installations/"):
			if err := verifyJWT(auth, key); err != nil {
				t.Errorf("bad app jwt: %v", err)
				http.Error(w, "bad jwt", 401)
				return
			}
			g.tokens++
			json.NewEncoder(w).Encode(map[string]any{"token": "tok-" + strconv.Itoa(g.tokens), "expires_at": t0.Add(time.Hour)})
			return
		case !strings.HasPrefix(auth, "tok-"):
			http.Error(w, "unauthorized", 401)
			return
		}
		g.calls = append(g.calls, call{r.Method, r.URL.RequestURI(), body})
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/check-runs"):
			g.nextID++
			json.NewEncoder(w).Encode(map[string]any{"id": g.nextID})
		case r.Method == http.MethodPatch && strings.Contains(path, "/check-runs/"):
			id, _ := strconv.ParseInt(path[strings.LastIndex(path, "/")+1:], 10, 64)
			if g.missingCR[id] {
				http.Error(w, "nope", 404)
				return
			}
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pulls"):
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			lo, hi := (page-1)*per, page*per
			if lo > len(g.pulls) {
				lo = len(g.pulls)
			}
			if hi > len(g.pulls) {
				hi = len(g.pulls)
			}
			json.NewEncoder(w).Encode(g.pulls[lo:hi])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func verifyJWT(tok string, pub *rsa.PublicKey) error {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return fmt.Errorf("want 3 parts")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return err
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss      string
		Iat, Exp int64
	}
	_ = json.Unmarshal([]byte(strings.ToLower(string(raw))), &c)
	if c.Iss != "42" || c.Exp-c.Iat > 600+60 || c.Exp <= c.Iat {
		return fmt.Errorf("bad claims %s", raw)
	}
	return nil
}

func (g *fakeGH) checkCalls() []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []call
	for _, c := range g.calls {
		if strings.Contains(c.Path, "/check-runs") {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	t   *testing.T
	st  *store.SQLite
	gh  *fakeGH
	svc *Service
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), t.TempDir()+"/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, st: st, gh: newFakeGH(t, &key.PublicKey), now: t0}
	cl, err := NewClient(Config{AppID: 42, PrivateKey: key, BaseURL: e.gh.URL, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = NewService(ServiceConfig{Store: st, Engine: fakeEngine{}, API: cl, WebhookSecret: []byte(secret), Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) seed() model.Repo {
	e.t.Helper()
	ctx := context.Background()
	must(e.t, e.st.UpsertInstallation(ctx, model.Installation{ID: 7, AccountLogin: "acme", AccountID: 1, AccountType: "Organization"}))
	r := model.Repo{ID: 55, InstallationID: 7, Owner: "acme", Name: "app", DefaultBranch: "main"}
	must(e.t, e.st.UpsertRepo(ctx, r))
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) freeze() model.Freeze {
	f, err := e.st.CreateFreeze(context.Background(), model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 7, Reason: "release", CreatedBy: "bob"})
	must(e.t, err)
	return f
}

// deliver sends a signed webhook and returns the status code.
func (e *env) deliver(event string, payload any) int {
	e.t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sign(body))
	rec := httptest.NewRecorder()
	e.svc.ServeHTTP(rec, req)
	return rec.Code
}

func sign(body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

type m = map[string]any

func prEvent(action string, n int, sha string, merged bool) m {
	return m{
		"action":       action,
		"installation": m{"id": 7, "account": m{"login": "acme", "id": 1, "type": "Organization"}},
		"repository":   m{"id": 55, "name": "app", "owner": m{"login": "acme"}, "default_branch": "main"},
		"pull_request": m{"number": n, "merged": merged, "head": m{"sha": sha}},
	}
}

func TestSignature(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"zen":"x"}`)
	for name, hdr := range map[string]string{"missing": "", "wrong": "sha256=00", "noprefix": hex.EncodeToString(body), "bad secret": func() string {
		mm := hmac.New(sha256.New, []byte("other"))
		mm.Write(body)
		return "sha256=" + hex.EncodeToString(mm.Sum(nil))
	}()} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature-256", hdr)
		req.Header.Set("X-GitHub-Event", "ping")
		rec := httptest.NewRecorder()
		e.svc.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("%s: code %d, want 401", name, rec.Code)
		}
	}
	if c := e.deliver("ping", m{"zen": "x"}); c != 204 {
		t.Errorf("ping: %d", c)
	}
	rec := httptest.NewRecorder()
	e.svc.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 405 {
		t.Errorf("GET: %d", rec.Code)
	}
	if c := e.deliver("pull_request", "not an object"); c != 400 {
		t.Errorf("bad payload: %d", c)
	}
}

func TestPullRequestLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Not frozen: success; repo and installation are created from the payload
	// even though no installation event was seen.
	if c := e.deliver("pull_request", prEvent("opened", 9, "sha1", false)); c != 204 {
		t.Fatalf("code %d", c)
	}
	calls := e.gh.checkCalls()
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Body["conclusion"] != "success" ||
		calls[0].Body["head_sha"] != "sha1" || calls[0].Body["name"] != "frozen-throne" || calls[0].Body["status"] != "completed" {
		t.Fatalf("calls: %+v", calls)
	}
	pr, err := e.st.GetPullRequest(ctx, 55, 9)
	must(t, err)
	if pr.CheckRun != 101 || pr.Conclusion != "success" || pr.State != model.PROpen {
		t.Fatalf("pr: %+v", pr)
	}

	// Frozen + same SHA (e.g. edited): the existing Check Run is updated.
	f := e.freeze()
	e.deliver("pull_request", prEvent("edited", 9, "sha1", false))
	calls = e.gh.checkCalls()
	last := calls[len(calls)-1]
	if last.Method != "PATCH" || !strings.HasSuffix(last.Path, "/check-runs/101") || last.Body["conclusion"] != "failure" {
		t.Fatalf("want PATCH failure, got %+v", last)
	}
	if s := last.Body["output"].(m)["summary"].(string); !strings.Contains(s, "Organization freeze: release") {
		t.Errorf("summary missing reason: %s", s)
	}

	// New push: a fresh Check Run on the new SHA.
	e.deliver("pull_request", prEvent("synchronize", 9, "sha2", false))
	calls = e.gh.checkCalls()
	last = calls[len(calls)-1]
	if last.Method != "POST" || last.Body["head_sha"] != "sha2" || last.Body["conclusion"] != "failure" {
		t.Fatalf("want POST on sha2, got %+v", last)
	}

	// Bypass survives the push and shows as success with its reason.
	_, err = e.st.CreateBypass(ctx, model.Bypass{RepoID: 55, PRNumber: 9, Reason: "hotfix for outage", CreatedBy: "alice"})
	must(t, err)
	e.deliver("pull_request", prEvent("synchronize", 9, "sha3", false))
	last = e.gh.checkCalls()[len(e.gh.checkCalls())-1]
	if last.Body["conclusion"] != "success" || !strings.Contains(last.Body["output"].(m)["summary"].(string), "hotfix for outage") {
		t.Fatalf("bypass check: %+v", last)
	}
	// ...and only for that PR.
	e.deliver("pull_request", prEvent("opened", 10, "sha10", false))
	last = e.gh.checkCalls()[len(e.gh.checkCalls())-1]
	if last.Body["conclusion"] != "failure" {
		t.Fatalf("other PR should fail: %+v", last)
	}

	// Thaw: the check passes and the bypass ends with reason "thawed".
	_, err = e.st.EndFreeze(ctx, f.ID, store.ThawInfo{By: "bob", At: e.now})
	must(t, err)
	e.deliver("pull_request", prEvent("synchronize", 9, "sha4", false))
	last = e.gh.checkCalls()[len(e.gh.checkCalls())-1]
	if last.Body["conclusion"] != "success" {
		t.Fatalf("thawed: %+v", last)
	}
	bs, err := e.st.ListBypasses(ctx, store.BypassFilter{InstallationID: 7})
	must(t, err)
	if len(bs) != 1 || bs[0].Active() || bs[0].EndReason != model.BypassEndedThawed {
		t.Fatalf("bypass: %+v", bs)
	}
	au, err := e.st.ListAudit(ctx, store.AuditFilter{InstallationID: 7, Action: model.AuditBypassEnd})
	must(t, err)
	if len(au) != 1 || au[0].PRNumber == nil || *au[0].PRNumber != 9 {
		t.Fatalf("audit: %+v", au)
	}
}

func TestPullRequestClosedEndsBypass(t *testing.T) {
	for _, tc := range []struct {
		merged bool
		state  model.PRState
		reason model.BypassEndReason
	}{{true, model.PRMerged, model.BypassEndedMerged}, {false, model.PRClosed, model.BypassEndedClosed}} {
		e := newEnv(t)
		ctx := context.Background()
		e.seed()
		e.freeze()
		e.deliver("pull_request", prEvent("opened", 3, "s", false))
		_, err := e.st.CreateBypass(ctx, model.Bypass{RepoID: 55, PRNumber: 3, Reason: "r"})
		must(t, err)
		if c := e.deliver("pull_request", prEvent("closed", 3, "s", tc.merged)); c != 204 {
			t.Fatalf("code %d", c)
		}
		pr, _ := e.st.GetPullRequest(ctx, 55, 3)
		if pr.State != tc.state {
			t.Errorf("state %s want %s", pr.State, tc.state)
		}
		bs, _ := e.st.ListBypasses(ctx, store.BypassFilter{InstallationID: 7})
		if len(bs) != 1 || bs[0].Active() || bs[0].EndReason != tc.reason {
			t.Errorf("bypass: %+v", bs)
		}
	}
}

func TestMissingCheckRunIsRecreated(t *testing.T) {
	e := newEnv(t)
	e.deliver("pull_request", prEvent("opened", 1, "s", false))
	e.gh.missingCR[101] = true
	e.deliver("pull_request", prEvent("edited", 1, "s", false))
	calls := e.gh.checkCalls()
	if len(calls) != 3 || calls[1].Method != "PATCH" || calls[2].Method != "POST" {
		t.Fatalf("calls: %+v", calls)
	}
	pr, _ := e.st.GetPullRequest(context.Background(), 55, 1)
	if pr.CheckRun != 102 {
		t.Errorf("check run %d", pr.CheckRun)
	}
}

func TestInstallationSync(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inst := m{"id": 7, "account": m{"login": "acme", "id": 1, "type": "Organization"}}
	repo := func(id int, n string) m {
		return m{"id": id, "name": n, "full_name": "acme/" + n, "private": true}
	}
	e.deliver("installation", m{"action": "created", "installation": inst, "repositories": []m{repo(1, "a"), repo(2, "b")}})
	got, err := e.st.GetInstallation(ctx, 7)
	must(t, err)
	if got.AccountLogin != "acme" || got.AccountType != "Organization" {
		t.Fatalf("%+v", got)
	}
	r, err := e.st.GetRepoByName(ctx, "acme", "a")
	must(t, err)
	if !r.Private || r.ID != 1 {
		t.Fatalf("%+v", r)
	}

	// Existing metadata (default branch, public status) survives thin payloads.
	must(t, e.st.UpsertRepo(ctx, model.Repo{ID: 1, InstallationID: 7, Owner: "acme", Name: "a", DefaultBranch: "trunk", Private: true}))
	must(t, e.st.SetPublicStatus(ctx, 1, true))
	e.deliver("installation_repositories", m{"action": "added", "installation": inst, "repositories_added": []m{repo(1, "a"), repo(3, "c")}})
	r, _ = e.st.GetRepo(ctx, 1)
	if r.DefaultBranch != "trunk" || !r.PublicStatus {
		t.Errorf("lost data: %+v", r)
	}
	if _, err := e.st.GetRepo(ctx, 3); err != nil {
		t.Error("repo 3 not added")
	}

	e.deliver("installation_repositories", m{"action": "removed", "installation": inst, "repositories_removed": []m{repo(2, "b")}})
	r, _ = e.st.GetRepo(ctx, 2)
	if r.RemovedAt == nil {
		t.Error("repo 2 not marked removed")
	}

	e.deliver("installation", m{"action": "suspend", "installation": inst})
	got, _ = e.st.GetInstallation(ctx, 7)
	if got.SuspendedAt == nil {
		t.Error("not suspended")
	}
	// Events of a suspended installation are ignored.
	e.deliver("pull_request", prEvent("opened", 1, "s", false))
	if n := len(e.gh.checkCalls()); n != 0 {
		t.Errorf("check posted while suspended: %d", n)
	}
	e.deliver("installation", m{"action": "unsuspend", "installation": inst})
	got, _ = e.st.GetInstallation(ctx, 7)
	if got.SuspendedAt != nil {
		t.Error("still suspended")
	}

	e.deliver("installation", m{"action": "deleted", "installation": inst})
	if _, err := e.st.GetInstallation(ctx, 7); err == nil {
		t.Error("installation not deleted")
	}
	// Deleting twice is fine.
	if c := e.deliver("installation", m{"action": "deleted", "installation": inst}); c != 204 {
		t.Errorf("second delete: %d", c)
	}
}

func TestCheckRunRerequestedAndMergeGroup(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.freeze()
	base := func() m {
		return m{
			"installation": m{"id": 7}, "repository": m{"id": 55, "name": "app", "owner": m{"login": "acme"}, "default_branch": "main"},
		}
	}

	// Rerequested on a PR's check.
	p := base()
	p["action"] = "rerequested"
	p["check_run"] = m{"name": "frozen-throne", "head_sha": "abc", "pull_requests": []m{{"number": 4}}}
	e.deliver("check_run", p)
	calls := e.gh.checkCalls()
	if len(calls) != 1 || calls[0].Body["head_sha"] != "abc" || calls[0].Body["conclusion"] != "failure" {
		t.Fatalf("%+v", calls)
	}
	if pr, err := e.st.GetPullRequest(context.Background(), 55, 4); err != nil || pr.CheckRun == 0 {
		t.Errorf("pr 4: %+v %v", pr, err)
	}

	// Other checks and other actions are ignored.
	p = base()
	p["action"] = "rerequested"
	p["check_run"] = m{"name": "lint", "head_sha": "abc"}
	e.deliver("check_run", p)
	p["action"] = "completed"
	p["check_run"] = m{"name": "frozen-throne", "head_sha": "abc"}
	e.deliver("check_run", p)
	if n := len(e.gh.checkCalls()); n != 1 {
		t.Errorf("calls: %d", n)
	}

	// Merge group: bypass of the queued PR (from head_ref) applies.
	_, err := e.st.CreateBypass(context.Background(), model.Bypass{RepoID: 55, PRNumber: 4, Reason: "urgent"})
	must(t, err)
	p = base()
	p["action"] = "checks_requested"
	p["merge_group"] = m{"head_sha": "mg1", "head_ref": "refs/heads/gh-readonly-queue/main/pr-4-0123abcd"}
	e.deliver("merge_group", p)
	p["merge_group"] = m{"head_sha": "mg2", "head_ref": "refs/heads/gh-readonly-queue/main/pr-5-0123abcd"}
	e.deliver("merge_group", p)
	p["action"] = "destroyed"
	e.deliver("merge_group", p)
	calls = e.gh.checkCalls()
	if len(calls) != 3 || calls[1].Body["conclusion"] != "success" || calls[2].Body["conclusion"] != "failure" {
		t.Fatalf("%+v", calls)
	}
}

func TestReevaluateRepo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.seed()
	for i := 1; i <= 150; i++ { // forces a second page
		e.gh.pulls = append(e.gh.pulls, m{"number": i, "draft": i == 2, "head": m{"sha": fmt.Sprintf("sha%d", i)}})
	}
	// PR 999 is open in the store but gone on GitHub; it holds a bypass.
	must(t, e.st.UpsertPullRequest(ctx, model.PullRequest{RepoID: 55, Number: 999, HeadSHA: "x", State: model.PROpen}))
	e.freeze()
	_, err := e.st.CreateBypass(ctx, model.Bypass{RepoID: 55, PRNumber: 999, Reason: "r"})
	must(t, err)

	res, err := e.svc.ReevaluateRepo(ctx, 55)
	must(t, err)
	if res.Open != 150 || res.Updated != 150 || res.Closed != 1 {
		t.Fatalf("%+v", res)
	}
	if e.gh.tokens != 1 {
		t.Errorf("installation token fetched %d times, want 1 (cached)", e.gh.tokens)
	}
	if bs, _ := e.st.ListBypasses(ctx, store.BypassFilter{InstallationID: 7, ActiveOnly: true}); len(bs) != 0 {
		t.Errorf("bypass for vanished PR still active")
	}
	if pr, _ := e.st.GetPullRequest(ctx, 55, 2); !pr.Draft || pr.Conclusion != "failure" {
		t.Errorf("pr 2: %+v", pr)
	}

	// Second run: nothing changed, no Check Run writes.
	before := len(e.gh.checkCalls())
	res, err = e.svc.ReevaluateRepo(ctx, 55)
	must(t, err)
	if res.Updated != 0 || len(e.gh.checkCalls()) != before {
		t.Fatalf("unchanged run wrote: %+v", res)
	}

	// Thaw: every PR flips to success via PATCH.
	fs, _ := e.st.ListFreezes(ctx, store.FreezeFilter{InstallationID: 7, ActiveOnly: true})
	_, err = e.st.EndFreeze(ctx, fs[0].ID, store.ThawInfo{By: "bob", At: e.now})
	must(t, err)
	res, err = e.svc.ReevaluateRepo(ctx, 55)
	must(t, err)
	if res.Updated != 150 {
		t.Fatalf("%+v", res)
	}
	calls := e.gh.checkCalls()
	if last := calls[len(calls)-1]; last.Method != "PATCH" || last.Body["conclusion"] != "success" {
		t.Errorf("%+v", last)
	}
}

func TestTokenRefresh(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.deliver("pull_request", prEvent("opened", 1, "s", false))
	e.now = t0.Add(30 * time.Minute)
	e.deliver("pull_request", prEvent("edited", 1, "s", false))
	if e.gh.tokens != 1 {
		t.Errorf("tokens %d, want 1", e.gh.tokens)
	}
	e.now = t0.Add(time.Hour - 30*time.Second) // inside the refresh skew
	e.deliver("pull_request", prEvent("edited", 1, "s", false))
	if e.gh.tokens != 2 {
		t.Errorf("tokens %d, want 2", e.gh.tokens)
	}
}

func TestParsePrivateKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	p1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for _, p := range [][]byte{p1, p8} {
		k, err := ParsePrivateKey(p)
		if err != nil || !k.Equal(key) {
			t.Errorf("parse: %v", err)
		}
	}
	if _, err := ParsePrivateKey([]byte("junk")); err == nil {
		t.Error("want error")
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(ServiceConfig{}); err == nil {
		t.Error("want error")
	}
	if _, err := NewClient(Config{}); err == nil {
		t.Error("want error")
	}
}
