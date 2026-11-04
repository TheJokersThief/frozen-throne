package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

func newStore(t *testing.T) *SQLite {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed creates installation inst with repos 100 and 101.
func seed(t *testing.T, s Store, inst int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertInstallation(ctx, model.Installation{ID: inst, AccountLogin: "acme", AccountID: 1, AccountType: "Organization"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{inst*10 + 0, inst*10 + 1} {
		if err := s.UpsertRepo(ctx, model.Repo{ID: id, InstallationID: inst, Owner: "acme", Name: fmt.Sprintf("repo%d", id)}); err != nil {
			t.Fatal(err)
		}
	}
}

func i64(v int64) *int64 { return &v }

func TestMigrateIdempotent(t *testing.T) {
	s := newStore(t)
	if err := Migrate(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
}

func TestReposAndPublicStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	repos, err := s.ListRepos(ctx, RepoFilter{InstallationID: 1})
	if err != nil || len(repos) != 2 {
		t.Fatalf("repos=%v err=%v", repos, err)
	}
	if repos[0].PublicStatus {
		t.Fatal("public status must default to off")
	}
	if err := s.SetPublicStatus(ctx, repos[0].ID, true); err != nil {
		t.Fatal(err)
	}
	// Re-sync from GitHub must not reset the opt-in.
	if err := s.UpsertRepo(ctx, repos[0]); err != nil {
		t.Fatal(err)
	}
	pub, _ := s.ListRepos(ctx, RepoFilter{PublicStatusOnly: true})
	if len(pub) != 1 || pub[0].ID != repos[0].ID {
		t.Fatalf("public repos = %v", pub)
	}
	if got, err := s.ListRepos(ctx, RepoFilter{Query: "REPO10"}); err != nil || len(got) != 1 {
		t.Fatalf("query: %v %v", got, err)
	}
	if err := s.MarkReposRemoved(ctx, 1, []int64{repos[0].ID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRepos(ctx, RepoFilter{InstallationID: 1}); len(got) != 1 {
		t.Fatalf("removed repo still listed: %v", got)
	}
	if _, err := s.GetRepoByName(ctx, "ACME", repos[1].Name); err != nil {
		t.Fatal(err)
	}
}

func TestFreezeLifecycleAndUniqueness(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)

	org, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, Reason: "release", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "bob"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("second org freeze: %v", err)
	}
	if _, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeRepo, InstallationID: 1, CreatedBy: "bob"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("repo freeze without repo: %v", err)
	}
	rf, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeRepo, InstallationID: 1, RepoID: i64(10), CreatedBy: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	// Org and repo freezes are independent.
	if got, err := s.ActiveOrgFreeze(ctx, 1); err != nil || got.ID != org.ID {
		t.Fatalf("org: %v %v", got, err)
	}
	if _, err := s.EndFreeze(ctx, org.ID, ThawInfo{By: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveOrgFreeze(ctx, 1); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("org still active: %v", err)
	}
	if got, err := s.ActiveRepoFreeze(ctx, 10); err != nil || got.ID != rf.ID {
		t.Fatalf("thawing org must not cancel repo freeze: %v %v", got, err)
	}
	if _, err := s.EndFreeze(ctx, org.ID, ThawInfo{By: "alice"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("double thaw: %v", err)
	}
	// A new org freeze is possible after thaw; history is kept.
	if _, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListFreezes(ctx, FreezeFilter{InstallationID: 1})
	active, _ := s.ListFreezes(ctx, FreezeFilter{InstallationID: 1, ActiveOnly: true})
	if len(all) != 3 || len(active) != 2 {
		t.Fatalf("all=%d active=%d", len(all), len(active))
	}
}

func TestOrgFreezeCoversReposAddedLater(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	if _, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "a"}); err != nil {
		t.Fatal(err)
	}
	// Repo added afterwards: the org freeze is found via its installation.
	if err := s.UpsertRepo(ctx, model.Repo{ID: 555, InstallationID: 1, Owner: "acme", Name: "late"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetRepo(ctx, 555)
	if _, err := s.ActiveOrgFreeze(ctx, r.InstallationID); err != nil {
		t.Fatalf("org freeze should cover late repo: %v", err)
	}
}

func TestMultipleInstallationsIsolated(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	seed(t, s, 2)
	if _, err := s.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveOrgFreeze(ctx, 2); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("installation 2 must not be frozen: %v", err)
	}
	if err := s.DeleteInstallation(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if repos, _ := s.ListRepos(ctx, RepoFilter{InstallationID: 1}); len(repos) != 0 {
		t.Fatal("repos should cascade")
	}
	if repos, _ := s.ListRepos(ctx, RepoFilter{InstallationID: 2}); len(repos) != 2 {
		t.Fatal("other installation affected")
	}
}

func TestSchedules(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	start := time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC)
	one, err := s.CreateSchedule(ctx, model.Schedule{InstallationID: 1, Name: "xmas", Kind: model.ScheduleOneOff, Timezone: "UTC", Enabled: true,
		StartsAt: &start, EndsAt: ptrTime(start.Add(48 * time.Hour)), CreatedBy: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !one.StartsAt.Equal(start) {
		t.Fatalf("starts_at roundtrip: %v", one.StartsAt)
	}
	rec, err := s.CreateSchedule(ctx, model.Schedule{InstallationID: 1, RepoID: i64(10), Kind: model.ScheduleRecurring, Timezone: "Europe/Dublin",
		Enabled: true, Weekdays: model.WeekdaysOf(time.Friday, time.Saturday), StartTime: "17:00", EndTime: "09:00", CreatedBy: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Weekdays.Has(time.Friday) || rec.Weekdays.Has(time.Monday) {
		t.Fatalf("weekdays = %b", rec.Weekdays)
	}
	for name, bad := range map[string]model.Schedule{
		"tz":       {InstallationID: 1, Kind: model.ScheduleRecurring, Timezone: "Mars/Base", Weekdays: 1, StartTime: "01:00", EndTime: "02:00"},
		"time":     {InstallationID: 1, Kind: model.ScheduleRecurring, Timezone: "UTC", Weekdays: 1, StartTime: "25:00", EndTime: "02:00"},
		"weekdays": {InstallationID: 1, Kind: model.ScheduleRecurring, Timezone: "UTC", StartTime: "01:00", EndTime: "02:00"},
		"order":    {InstallationID: 1, Kind: model.ScheduleOneOff, Timezone: "UTC", StartsAt: ptrTime(start), EndsAt: ptrTime(start)},
	} {
		if _, err := s.CreateSchedule(ctx, bad); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	// Applies-to-repo includes org-wide + that repo's windows only.
	got, _ := s.ListSchedules(ctx, ScheduleFilter{InstallationID: 1, AppliesToRepoID: i64(11)})
	if len(got) != 1 || got[0].ID != one.ID {
		t.Fatalf("applies to 11: %v", got)
	}
	got, _ = s.ListSchedules(ctx, ScheduleFilter{InstallationID: 1, AppliesToRepoID: i64(10)})
	if len(got) != 2 {
		t.Fatalf("applies to 10: %v", got)
	}
	rec.Enabled = false
	if _, err := s.UpdateSchedule(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListSchedules(ctx, ScheduleFilter{InstallationID: 1, EnabledOnly: true}); len(got) != 1 {
		t.Fatalf("enabled only: %v", got)
	}
	if err := s.DeleteSchedule(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(ctx, one.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestBypasses(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	if _, err := s.CreateBypass(ctx, model.Bypass{RepoID: 10, PRNumber: 5, Reason: "  ", CreatedBy: "a"}); !errors.Is(err, model.ErrReasonRequired) {
		t.Fatalf("blank reason: %v", err)
	}
	b, err := s.CreateBypass(ctx, model.Bypass{RepoID: 10, PRNumber: 5, Reason: "hotfix", CreatedBy: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBypass(ctx, model.Bypass{RepoID: 10, PRNumber: 5, Reason: "again", CreatedBy: "a"}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("duplicate active: %v", err)
	}
	if got, err := s.ActiveBypass(ctx, 10, 5); err != nil || got.ID != b.ID {
		t.Fatalf("active: %v %v", got, err)
	}
	if _, err := s.ActiveBypass(ctx, 10, 6); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("other PR must not be bypassed")
	}
	if n, err := s.EndBypassesForPR(ctx, 10, 5, model.BypassEndedMerged, time.Now()); err != nil || n != 1 {
		t.Fatalf("end for PR: %d %v", n, err)
	}
	if _, err := s.ActiveBypass(ctx, 10, 5); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("bypass should be over")
	}
	// A new bypass is allowed once the old one ended; repo-wide end works.
	for _, pr := range []int{5, 6} {
		if _, err := s.CreateBypass(ctx, model.Bypass{RepoID: 10, PRNumber: pr, Reason: "x", CreatedBy: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.EndBypassesForRepo(ctx, 10, model.BypassEndedThawed, time.Now()); n != 2 {
		t.Fatalf("ended %d", n)
	}
	if _, err := s.EndBypass(ctx, b.ID, model.BypassEndedRevoked, time.Now()); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("end twice: %v", err)
	}
	hist, _ := s.ListBypasses(ctx, BypassFilter{InstallationID: 1})
	if len(hist) != 3 {
		t.Fatalf("history = %d", len(hist))
	}
}

func TestAuditAppendOnlyAndPaging(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	for i := 0; i < 5; i++ {
		if _, err := s.AppendAudit(ctx, model.AuditEntry{InstallationID: 1, RepoID: i64(10), Action: model.AuditFreeze, ActorLogin: "alice", Reason: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendAudit(ctx, model.AuditEntry{InstallationID: 1, Action: ""}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("empty action: %v", err)
	}
	page1, _ := s.ListAudit(ctx, AuditFilter{InstallationID: 1, Limit: 3})
	page2, _ := s.ListAudit(ctx, AuditFilter{InstallationID: 1, Limit: 3, BeforeID: page1[2].ID})
	if len(page1) != 3 || len(page2) != 2 || page1[0].ID < page1[1].ID {
		t.Fatalf("paging: %d %d", len(page1), len(page2))
	}
	if _, err := s.exec(ctx, `UPDATE audit_log SET reason = 'tampered'`); err == nil {
		t.Fatal("audit log must be append-only")
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.UpsertUser(ctx, model.User{ID: 7, Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.CreateSession(ctx, model.Session{ID: "s1", UserID: 7, GitHubToken: []byte("tok"), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "s1", now)
	if err != nil || string(got.GitHubToken) != "tok" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := s.GetSession(ctx, "s1", now.Add(2*time.Hour)); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if n, _ := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("expired deleted = %d", n)
	}
}

func TestSlackConfig(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	if _, err := s.GetSlackConfig(ctx, 1); !errors.Is(err, model.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.UpsertSlackConfig(ctx, model.SlackConfig{InstallationID: 1, WebhookURL: "https://hooks.slack.test/x", Enabled: true, Events: []string{"freeze", "thaw"}}); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetSlackConfig(ctx, 1)
	if err != nil || len(c.Events) != 2 || !c.Enabled {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestPullRequests(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	for n := 1; n <= 3; n++ {
		if err := s.UpsertPullRequest(ctx, model.PullRequest{RepoID: 10, Number: n, HeadSHA: "abc"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPullRequestCheck(ctx, 10, 1, 99, "failure", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A webhook upsert (no check info) must not erase the recorded check run.
	if err := s.UpsertPullRequest(ctx, model.PullRequest{RepoID: 10, Number: 1, HeadSHA: "def"}); err != nil {
		t.Fatal(err)
	}
	pr, _ := s.GetPullRequest(ctx, 10, 1)
	if pr.CheckRun != 99 || pr.Conclusion != "failure" || pr.HeadSHA != "def" {
		t.Fatalf("%+v", pr)
	}
	if err := s.SetPullRequestState(ctx, 10, 2, model.PRMerged, time.Now()); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.ListOpenPullRequests(ctx, 10); len(open) != 2 {
		t.Fatalf("open = %d", len(open))
	}
}

func TestWithTxRollback(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(tx Store) error {
		if _, err := tx.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "a"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := s.ActiveOrgFreeze(ctx, 1); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("freeze should have rolled back")
	}
	err = s.WithTx(ctx, func(tx Store) error {
		f, err := tx.CreateFreeze(ctx, model.Freeze{Scope: model.FreezeScopeOrg, InstallationID: 1, CreatedBy: "a"})
		if err != nil {
			return err
		}
		_, err = tx.AppendAudit(ctx, model.AuditEntry{InstallationID: 1, Action: model.AuditFreeze, ActorLogin: "a", Scope: f.Scope})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveOrgFreeze(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

// A transaction that reads and then writes must not fail with SQLITE_BUSY just
// because another connection committed a write in between (the reconciler and
// webhook handlers write while API requests run). Write transactions therefore
// take the write lock when they begin and wait for it (busy_timeout).
func TestWithTxReadThenWriteSurvivesConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seed(t, s, 1)

	err := s.WithTx(ctx, func(tx Store) error {
		if _, err := tx.GetInstallation(ctx, 1); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			done <- s.UpsertInstallation(ctx, model.Installation{ID: 2, AccountLogin: "other", AccountID: 2, AccountType: "Organization"})
		}()
		select {
		case err := <-done: // the other writer got in first
			if err != nil {
				return err
			}
		case <-time.After(300 * time.Millisecond): // it is waiting for us, as it should
			t.Cleanup(func() { <-done })
		}
		return tx.UpsertInstallation(ctx, model.Installation{ID: 3, AccountLogin: "third", AccountID: 3, AccountType: "Organization"})
	})
	if err != nil {
		t.Fatalf("read-then-write transaction: %v", err)
	}
}
