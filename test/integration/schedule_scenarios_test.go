//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

// Scenario 3a: a one-off window flips the check to failure when it opens and
// back to success when it closes, with no API call at either boundary. The
// app's real clock is used, so the window is a few seconds long; the test
// polls with a timeout and never sleeps for a fixed time.
func TestScenarioOneOffWindowBoundaries(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	repo := e.Primary()
	sha := e.SHA("head")
	e.OpenPR(1, sha)
	e.WaitVerdict(repo, sha, "success")

	maint := stack.Login(t, e.Maintainer)
	start := time.Now().Add(3 * time.Second).Truncate(time.Second)
	end := start.Add(8 * time.Second)
	e.expect(maint, 2, http.MethodPost, e.repoPath(repo)+"/schedules", map[string]any{
		"name": "deploy window", "reason": "quarterly deploy", "kind": "one_off", "timezone": "UTC",
		"starts_at": start.Format(time.RFC3339), "ends_at": end.Format(time.RFC3339),
	})
	if v := e.Verdict(repo, sha); v != "success" && time.Now().Before(start) {
		t.Fatalf("window not open yet but verdict = %s\n%s", v, e.Dump())
	}

	e.WaitVerdict(repo, sha, "failure")
	if now := time.Now(); now.Before(start) {
		t.Fatalf("check failed at %s, before the window opened at %s", now.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	e.WaitVerdict(repo, sha, "success")
	if now := time.Now(); now.Before(end) {
		t.Fatalf("check passed at %s, before the window closed at %s", now.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	}
}

// Scenario 3b: a recurring window is evaluated in its own timezone. Two repos
// get the same wall-clock window ("an hour either side of now" in zone A); the
// zone-A repo is frozen and the repo whose window is in a zone hours away is
// not.
func TestScenarioRecurringWindowHonoursTimezone(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	inZone := e.Primary()
	otherZone := e.NewRepo("elsewhere")
	inSHA, otherSHA := e.SHA("in"), e.SHA("other")
	e.OpenPRIn(inZone, 1, inSHA)
	e.OpenPRIn(otherZone, 1, otherSHA)
	e.WaitVerdict(inZone, inSHA, "success")
	e.WaitVerdict(otherZone, otherSHA, "success")

	zoneA, zoneB, startHHMM, endHHMM := recurringWindowZones(t, time.Now())
	t.Logf("window %s-%s: active in %s, not in %s", startHHMM, endHHMM, zoneA, zoneB)
	all := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

	maint := stack.Login(t, e.Maintainer)
	for _, c := range []struct {
		repo RepoRef
		zone string
	}{{inZone, zoneA}, {otherZone, zoneB}} {
		e.expect(maint, 2, http.MethodPost, e.repoPath(c.repo)+"/schedules", map[string]any{
			"name": "daily window", "reason": "tz test", "kind": "recurring", "timezone": c.zone,
			"weekdays": all, "start_time": startHHMM, "end_time": endHHMM,
		})
	}
	e.WaitVerdict(inZone, inSHA, "failure")
	// Re-run the other repo's check synchronously (a check_run rerequested
	// webhook is handled before it returns), so reading its verdict is not a
	// race against the reconciler.
	e.Deliver(webhooks.CheckRunRerequested(e.RepoParams(otherZone, 1, otherSHA)))
	var st struct {
		Frozen bool `json:"frozen"`
	}
	if code := maint.JSON(http.MethodGet, e.repoPath(otherZone), nil, &st); code != http.StatusOK || st.Frozen {
		t.Fatalf("API says repo with the window in %s is frozen=%v (status %d)", zoneB, st.Frozen, code)
	}
	if v := e.Verdict(otherZone, otherSHA); v != "success" {
		t.Fatalf("repo with the window in %s should not be frozen now, verdict = %s\n%s", zoneB, v, e.Dump())
	}
}

// recurringWindowZones picks a zone in which `now` falls mid-day and a second
// zone at least three hours away, and returns a window from one hour before to
// one hour after now in the first zone's wall clock (never crossing midnight).
func recurringWindowZones(t *testing.T, now time.Time) (active, inactive, start, end string) {
	t.Helper()
	zones := []string{"UTC", "Asia/Tokyo", "America/New_York", "Pacific/Kiritimati", "Pacific/Honolulu", "Europe/Berlin"}
	for _, a := range zones {
		la, err := time.LoadLocation(a)
		if err != nil {
			t.Fatal(err)
		}
		local := now.In(la)
		if local.Hour() < 2 || local.Hour() > 21 {
			continue
		}
		for _, b := range zones {
			lb, _ := time.LoadLocation(b)
			if diff := local.Hour() - now.In(lb).Hour(); diff >= 3 || diff <= -3 {
				if diff > 12 || diff < -12 {
					continue // wraps around midnight: still far apart but keep it simple
				}
				return a, b, local.Add(-time.Hour).Format("15:04"), local.Add(time.Hour).Format("15:04")
			}
		}
	}
	t.Fatalf("no suitable zone pair at %s", now)
	return
}
