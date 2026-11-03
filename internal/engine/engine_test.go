package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

const (
	instID int64 = 7
	repoID int64 = 100
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr[T any](v T) *T { return &v }

func baseRepo() model.Repo { return model.Repo{ID: repoID, InstallationID: instID} }

func manual(id int64, scope model.FreezeScope) *model.Freeze {
	f := &model.Freeze{ID: id, Scope: scope, InstallationID: instID, Reason: "r", CreatedAt: utc("2026-01-01 00:00")}
	if scope == model.FreezeScopeRepo {
		f.RepoID = ptr(repoID)
	}
	return f
}

func thawed(f *model.Freeze) *model.Freeze {
	c := *f
	c.EndedAt = ptr(utc("2026-01-02 00:00"))
	return &c
}

func recurring(id int64, tz string, days model.Weekdays, start, end string) model.Schedule {
	return model.Schedule{
		ID: id, InstallationID: instID, Name: "w", Kind: model.ScheduleRecurring, Enabled: true,
		Timezone: tz, Weekdays: days, StartTime: start, EndTime: end,
	}
}

func oneOff(id int64, from, to string) model.Schedule {
	return model.Schedule{
		ID: id, InstallationID: instID, Name: "o", Kind: model.ScheduleOneOff, Enabled: true,
		StartsAt: ptr(utc(from)), EndsAt: ptr(utc(to)),
	}
}

func TestFreezeSourcesAndPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		org, repo *model.Freeze
		scheds    []model.Schedule
		frozen    bool
		kinds     []SourceKind
	}{
		{name: "nothing"},
		{name: "org only", org: manual(1, model.FreezeScopeOrg), frozen: true, kinds: []SourceKind{SourceOrgFreeze}},
		{name: "repo only", repo: manual(2, model.FreezeScopeRepo), frozen: true, kinds: []SourceKind{SourceRepoFreeze}},
		{name: "thawed org ignored", org: thawed(manual(1, model.FreezeScopeOrg))},
		{name: "thawed repo ignored", repo: thawed(manual(2, model.FreezeScopeRepo))},
		{
			name: "org freeze cannot be overridden by thawed repo freeze",
			org:  manual(1, model.FreezeScopeOrg), repo: thawed(manual(2, model.FreezeScopeRepo)),
			frozen: true, kinds: []SourceKind{SourceOrgFreeze},
		},
		{
			name: "thawing org leaves repo freeze",
			org:  thawed(manual(1, model.FreezeScopeOrg)), repo: manual(2, model.FreezeScopeRepo),
			frozen: true, kinds: []SourceKind{SourceRepoFreeze},
		},
		{
			name: "all three sources",
			org:  manual(1, model.FreezeScopeOrg), repo: manual(2, model.FreezeScopeRepo),
			scheds: []model.Schedule{oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")},
			frozen: true, kinds: []SourceKind{SourceOrgFreeze, SourceRepoFreeze, SourceSchedule},
		},
		{
			name:   "schedule alone",
			scheds: []model.Schedule{oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")},
			frozen: true, kinds: []SourceKind{SourceSchedule},
		},
		{
			name: "schedule for other repo ignored",
			scheds: []model.Schedule{func() model.Schedule {
				s := oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")
				s.RepoID = ptr(int64(999))
				return s
			}()},
		},
		{
			name: "schedule scoped to this repo applies",
			scheds: []model.Schedule{func() model.Schedule {
				s := oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")
				s.RepoID = ptr(repoID)
				return s
			}()},
			frozen: true, kinds: []SourceKind{SourceSchedule},
		},
		{
			name: "org-wide schedule of other installation ignored",
			scheds: []model.Schedule{func() model.Schedule {
				s := oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")
				s.InstallationID = 8
				return s
			}()},
		},
		{
			name:   "disabled schedule ignored (even if malformed)",
			scheds: []model.Schedule{{ID: 3, InstallationID: instID, Kind: model.ScheduleRecurring, Timezone: "Nope/Nope"}},
		},
	}
	at := utc("2026-06-01 12:00")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New().Evaluate(Input{Repo: baseRepo(), OrgFreeze: tc.org, RepoFreeze: tc.repo, Schedules: tc.scheds, At: at})
			if err != nil {
				t.Fatal(err)
			}
			if d.Frozen != tc.frozen || d.Blocked != tc.frozen || d.Bypassed {
				t.Fatalf("frozen=%v blocked=%v bypassed=%v, want frozen=%v", d.Frozen, d.Blocked, d.Bypassed, tc.frozen)
			}
			if len(d.Reasons) != len(tc.kinds) {
				t.Fatalf("reasons=%+v want kinds %v", d.Reasons, tc.kinds)
			}
			for i, k := range tc.kinds {
				if d.Reasons[i].Kind != k {
					t.Errorf("reason %d kind=%s want %s", i, d.Reasons[i].Kind, k)
				}
			}
		})
	}
}

func TestReasonDetails(t *testing.T) {
	org := manual(1, model.FreezeScopeOrg)
	org.Reason = "code red"
	s := oneOff(3, "2026-06-01 00:00", "2026-06-02 00:00")
	d, err := New().Evaluate(Input{Repo: baseRepo(), OrgFreeze: org, Schedules: []model.Schedule{s}, At: utc("2026-06-01 12:00")})
	if err != nil {
		t.Fatal(err)
	}
	if r := d.Reasons[0]; r.FreezeID != 1 || r.Text != "code red" || !r.Since.Equal(org.CreatedAt) || r.Until != nil {
		t.Errorf("org reason %+v", r)
	}
	if r := d.Reasons[1]; r.ScheduleID != 3 || r.Text != "o" || !r.Since.Equal(utc("2026-06-01 00:00")) || r.Until == nil || !r.Until.Equal(utc("2026-06-02 00:00")) {
		t.Errorf("schedule reason %+v", r)
	}
}

func TestOneOffBoundaries(t *testing.T) {
	s := oneOff(1, "2026-06-01 10:00", "2026-06-01 12:00")
	tests := []struct {
		at     string
		frozen bool
		next   string // "" = nil
	}{
		{"2026-06-01 09:59", false, "2026-06-01 10:00"},
		{"2026-06-01 10:00", true, "2026-06-01 12:00"}, // start inclusive
		{"2026-06-01 11:59", true, "2026-06-01 12:00"},
		{"2026-06-01 12:00", false, ""}, // end exclusive: auto-thaw
		{"2026-06-02 00:00", false, ""},
	}
	for _, tc := range tests {
		d, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, At: utc(tc.at)})
		if err != nil {
			t.Fatal(err)
		}
		if d.Frozen != tc.frozen {
			t.Errorf("%s frozen=%v want %v", tc.at, d.Frozen, tc.frozen)
		}
		checkNext(t, tc.at, d.NextChange, tc.next)
	}
}

func checkNext(t *testing.T, label string, got *time.Time, want string) {
	t.Helper()
	switch {
	case want == "" && got != nil:
		t.Errorf("%s next=%v want nil", label, got)
	case want != "" && got == nil:
		t.Errorf("%s next=nil want %s", label, want)
	case want != "" && !got.Equal(utc(want)):
		t.Errorf("%s next=%v want %s", label, got.UTC(), want)
	}
}

func TestOneOffInvalid(t *testing.T) {
	bad := []model.Schedule{
		{ID: 1, Kind: model.ScheduleOneOff, Enabled: true},
		oneOff(2, "2026-06-01 12:00", "2026-06-01 12:00"),
		oneOff(3, "2026-06-01 12:00", "2026-06-01 11:00"),
		{ID: 4, Kind: "weird", Enabled: true},
	}
	for _, s := range bad {
		s.InstallationID = instID
		if _, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, At: utc("2026-06-01 00:00")}); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("schedule %d: err=%v", s.ID, err)
		}
	}
	if _, err := New().Evaluate(Input{Repo: baseRepo()}); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("zero At: err=%v", err)
	}
}

// Recurring windows. Instants are UTC; zone offsets are noted per case.
func TestRecurring(t *testing.T) {
	weekdays := model.WeekdaysOf(time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday)
	friNight := recurring(1, "UTC", model.WeekdaysOf(time.Friday), "18:00", "09:00") // overnight, Fri -> Sat
	satNight := recurring(1, "UTC", model.WeekdaysOf(time.Saturday), "22:00", "04:00")
	sunNight := recurring(1, "UTC", model.WeekdaysOf(time.Sunday), "22:00", "04:00") // crosses the week boundary
	full := recurring(1, "UTC", model.WeekdaysOf(time.Monday), "00:00", "00:00")     // start==end: 24h

	validWindow := recurring(1, "UTC", weekdays, "09:00", "17:00")
	validWindow.ValidFrom = ptr("2026-06-10")  // Wed
	validWindow.ValidUntil = ptr("2026-06-12") // Fri

	// A Tuesday-anchored overnight window must not leak into Wednesday's start.
	tueNight := recurring(1, "UTC", model.WeekdaysOf(time.Tuesday), "23:00", "02:00")

	ny := recurring(1, "America/New_York", weekdays, "09:00", "17:00")  // EDT = UTC-4 in June
	dublin := recurring(1, "Europe/Dublin", weekdays, "09:00", "17:00") // IST = UTC+1 in June

	tests := []struct {
		name   string
		s      model.Schedule
		at     string
		frozen bool
		next   string
	}{
		// 2026-06-05 is a Friday.
		{"weekday inside", recurring(1, "UTC", weekdays, "09:00", "17:00"), "2026-06-05 10:00", true, "2026-06-05 17:00"},
		{"weekday start inclusive", recurring(1, "UTC", weekdays, "09:00", "17:00"), "2026-06-05 09:00", true, "2026-06-05 17:00"},
		{"weekday end exclusive, next Monday", recurring(1, "UTC", weekdays, "09:00", "17:00"), "2026-06-05 17:00", false, "2026-06-08 09:00"},
		{"before start", recurring(1, "UTC", weekdays, "09:00", "17:00"), "2026-06-05 08:59", false, "2026-06-05 09:00"},
		{"weekend skipped", recurring(1, "UTC", weekdays, "09:00", "17:00"), "2026-06-06 10:00", false, "2026-06-08 09:00"},
		{"timezone NY inside (13:00Z = 09:00 EDT)", ny, "2026-06-05 13:00", true, "2026-06-05 21:00"},
		{"timezone NY before (12:59Z)", ny, "2026-06-05 12:59", false, "2026-06-05 13:00"},
		{"timezone Dublin inside (08:00Z = 09:00 IST)", dublin, "2026-06-05 08:00", true, "2026-06-05 16:00"},
		{"timezone Dublin after (16:00Z)", dublin, "2026-06-05 16:00", false, "2026-06-08 08:00"},

		{"overnight evening", friNight, "2026-06-05 20:00", true, "2026-06-06 09:00"},
		{"overnight spills into Saturday", friNight, "2026-06-06 08:59", true, "2026-06-06 09:00"},
		{"overnight over after end", friNight, "2026-06-06 09:00", false, "2026-06-12 18:00"},
		{"overnight not anchored to Saturday", friNight, "2026-06-06 20:00", false, "2026-06-12 18:00"},
		{"Sat->Sun night inside Sunday", satNight, "2026-06-07 03:00", true, "2026-06-07 04:00"},
		{"Sun->Mon crosses week boundary", sunNight, "2026-06-08 03:00", true, "2026-06-08 04:00"},
		{"Sun->Mon starts Sunday", sunNight, "2026-06-07 23:00", true, "2026-06-08 04:00"},
		{"Sun->Mon not on Monday night", sunNight, "2026-06-08 23:00", false, "2026-06-14 22:00"},
		{"Tue night 23:00 ends Wed 02:00", tueNight, "2026-06-10 01:00", true, "2026-06-10 02:00"},
		{"Tue night not Wed 23:00", tueNight, "2026-06-10 23:30", false, "2026-06-16 23:00"},
		{"24h window", full, "2026-06-08 12:00", true, "2026-06-09 00:00"},
		{"24h window next Monday", full, "2026-06-09 12:00", false, "2026-06-15 00:00"},

		{"valid_from: before range", validWindow, "2026-06-09 10:00", false, "2026-06-10 09:00"},
		{"valid_from: first day", validWindow, "2026-06-10 10:00", true, "2026-06-10 17:00"},
		{"valid_until inclusive", validWindow, "2026-06-12 10:00", true, "2026-06-12 17:00"},
		{"valid_until: after range, no next", validWindow, "2026-06-12 17:00", false, ""},
		{"valid_until: later week", validWindow, "2026-06-15 10:00", false, ""},
		{"no weekdays never freezes", recurring(1, "UTC", 0, "09:00", "17:00"), "2026-06-05 10:00", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{tc.s}, At: utc(tc.at)})
			if err != nil {
				t.Fatal(err)
			}
			if d.Frozen != tc.frozen {
				t.Errorf("frozen=%v want %v", d.Frozen, tc.frozen)
			}
			checkNext(t, tc.at, d.NextChange, tc.next)
		})
	}
}

func TestEveryDayChainIsOpenEnded(t *testing.T) {
	s := recurring(1, "UTC", model.WeekdaysOf(0, 1, 2, 3, 4, 5, 6), "00:00", "00:00")
	d, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, At: utc("2026-06-05 10:00")})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Frozen || d.NextChange != nil || d.Reasons[0].Until != nil {
		t.Fatalf("%+v", d)
	}
	// Back-to-back windows do not produce a spurious change at midnight.
	s = recurring(1, "UTC", model.WeekdaysOf(time.Friday, time.Saturday), "12:00", "12:00")
	d, _ = New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, At: utc("2026-06-05 13:00")})
	checkNext(t, "merged", d.NextChange, "2026-06-07 12:00")
}

// DST cases. Offsets: America/New_York springs forward 2026-03-08 02:00 EST
// (07:00Z) and falls back 2026-11-01 02:00 EDT (06:00Z). Europe/Dublin springs
// forward 2026-03-29 01:00 GMT (01:00Z) and falls back 2026-10-25 02:00 IST
// (01:00Z). Australia/Lord_Howe shifts by 30 minutes.
func TestDST(t *testing.T) {
	sun := model.WeekdaysOf(time.Sunday)
	tests := []struct {
		name  string
		s     model.Schedule
		probe []struct {
			at     string
			frozen bool
			next   string
		}
	}{
		{
			// Window 01:00-04:00 local on the spring-forward Sunday lasts 2h:
			// 01:00 EST = 06:00Z .. 04:00 EDT = 08:00Z.
			name: "NY spring forward, window spans gap",
			s:    recurring(1, "America/New_York", sun, "01:00", "04:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-08 05:59", false, "2026-03-08 06:00"},
				{"2026-03-08 06:00", true, "2026-03-08 08:00"},
				{"2026-03-08 07:30", true, "2026-03-08 08:00"},
				{"2026-03-08 07:59", true, "2026-03-08 08:00"},
				{"2026-03-08 08:00", false, "2026-03-15 05:00"}, // next Sunday: 01:00 EDT = 05:00Z
			},
		},
		{
			// Start inside the gap (02:30 doesn't exist) begins at the transition
			// 07:00Z; end 03:30 EDT = 07:30Z.
			name: "NY spring forward, start in gap",
			s:    recurring(1, "America/New_York", sun, "02:30", "03:30"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-08 06:59", false, "2026-03-08 07:00"},
				{"2026-03-08 07:00", true, "2026-03-08 07:30"},
				{"2026-03-08 07:30", false, "2026-03-15 06:30"}, // 02:30 EDT = 06:30Z
			},
		},
		{
			// End inside the gap: 01:00 EST..02:30(nonexistent) ends at transition.
			name: "NY spring forward, end in gap",
			s:    recurring(1, "America/New_York", sun, "01:00", "02:30"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-08 06:30", true, "2026-03-08 07:00"},
				{"2026-03-08 07:00", false, "2026-03-15 05:00"},
			},
		},
		{
			// Both ends in the gap: empty window on that date only.
			name: "NY spring forward, window entirely in gap is empty",
			s:    recurring(1, "America/New_York", sun, "02:10", "02:40"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-08 06:30", false, "2026-03-15 06:10"},
				{"2026-03-08 07:10", false, "2026-03-15 06:10"},
			},
		},
		{
			// 00:30 EDT = 04:30Z .. 03:00 EST = 08:00Z: 2.5h of wall clock but 3.5h
			// elapsed because 01:00-02:00 repeats.
			name: "NY fall back, window spans overlap",
			s:    recurring(1, "America/New_York", sun, "00:30", "03:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-11-01 04:29", false, "2026-11-01 04:30"},
				{"2026-11-01 04:30", true, "2026-11-01 08:00"},
				{"2026-11-01 06:30", true, "2026-11-01 08:00"}, // second 01:30
				{"2026-11-01 07:59", true, "2026-11-01 08:00"},
				{"2026-11-01 08:00", false, "2026-11-08 05:30"},
			},
		},
		{
			// Start in the overlap -> first occurrence (05:30Z = 01:30 EDT).
			name: "NY fall back, start in overlap uses first occurrence",
			s:    recurring(1, "America/New_York", sun, "01:30", "03:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-11-01 05:29", false, "2026-11-01 05:30"},
				{"2026-11-01 05:30", true, "2026-11-01 08:00"},
			},
		},
		{
			// End in the overlap -> second occurrence (06:30Z = 01:30 EST).
			name: "NY fall back, end in overlap uses second occurrence",
			s:    recurring(1, "America/New_York", sun, "00:00", "01:30"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-11-01 05:45", true, "2026-11-01 06:30"},
				{"2026-11-01 06:29", true, "2026-11-01 06:30"},
				{"2026-11-01 06:30", false, "2026-11-08 05:00"},
			},
		},
		{
			// Overnight Sat 22:00 -> Sun 04:00 across the spring gap: 22:00 EST =
			// 03:00Z Sat->... Sat 2026-03-07 22:00 EST = 2026-03-08 03:00Z; Sun
			// 04:00 EDT = 08:00Z. 5h elapsed for a "6h" wall span.
			name: "NY overnight window crossing spring forward",
			s:    recurring(1, "America/New_York", model.WeekdaysOf(time.Saturday), "22:00", "04:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-08 02:59", false, "2026-03-08 03:00"},
				{"2026-03-08 03:00", true, "2026-03-08 08:00"},
				{"2026-03-08 07:59", true, "2026-03-08 08:00"},
				{"2026-03-08 08:00", false, "2026-03-15 02:00"}, // next Sat 22:00 EDT = 02:00Z Sun
			},
		},
		{
			// Overnight across fall back: Sat 22:00 EDT = 02:00Z Sun .. Sun 04:00 EST
			// = 09:00Z.
			name: "NY overnight window crossing fall back",
			s:    recurring(1, "America/New_York", model.WeekdaysOf(time.Saturday), "22:00", "04:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-11-01 01:59", false, "2026-11-01 02:00"},
				{"2026-11-01 02:00", true, "2026-11-01 09:00"},
				{"2026-11-01 08:59", true, "2026-11-01 09:00"},
				{"2026-11-01 09:00", false, "2026-11-08 03:00"}, // Sat 22:00 EST = 03:00Z Sun
			},
		},
		{
			// Dublin springs forward at 01:00Z: 00:30 GMT = 00:30Z; 03:00 IST = 02:00Z.
			name: "Dublin spring forward",
			s:    recurring(1, "Europe/Dublin", sun, "00:30", "03:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-03-29 00:29", false, "2026-03-29 00:30"},
				{"2026-03-29 00:30", true, "2026-03-29 02:00"},
				{"2026-03-29 01:59", true, "2026-03-29 02:00"},
				{"2026-03-29 02:00", false, "2026-04-04 23:30"}, // 00:30 IST Sun = 23:30Z Sat
			},
		},
		{
			// Dublin falls back at 01:00Z. Window 01:30-02:30 local: 01:30 occurs
			// twice (00:30Z IST, 01:30Z GMT); start = first (00:30Z), end 02:30 GMT
			// = 02:30Z.
			name: "Dublin fall back",
			s:    recurring(1, "Europe/Dublin", sun, "01:30", "02:30"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-10-25 00:29", false, "2026-10-25 00:30"},
				{"2026-10-25 00:30", true, "2026-10-25 02:30"},
				{"2026-10-25 02:29", true, "2026-10-25 02:30"},
				{"2026-10-25 02:30", false, "2026-11-01 01:30"},
			},
		},
		{
			// Lord Howe shifts 30min: springs forward 2026-10-04 02:00 LHST(+10:30)
			// -> 02:30 LHDT(+11). 02:15 doesn't exist.
			// Transition instant = 2026-10-03 15:30Z.
			name: "Lord Howe 30-minute gap",
			s:    recurring(1, "Australia/Lord_Howe", sun, "02:15", "04:00"),
			probe: []struct {
				at     string
				frozen bool
				next   string
			}{
				{"2026-10-03 15:29", false, "2026-10-03 15:30"},
				{"2026-10-03 15:30", true, "2026-10-03 17:00"}, // 04:00 +11 = 17:00Z
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.probe {
				d, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{tc.s}, At: utc(p.at)})
				if err != nil {
					t.Fatal(err)
				}
				if d.Frozen != p.frozen {
					t.Errorf("%s frozen=%v want %v", p.at, d.Frozen, p.frozen)
				}
				checkNext(t, p.at, d.NextChange, p.next)
			}
		})
	}
}

func TestRecurringInvalid(t *testing.T) {
	mk := func(mut func(*model.Schedule)) model.Schedule {
		s := recurring(1, "UTC", model.WeekdaysOf(time.Monday), "09:00", "17:00")
		mut(&s)
		return s
	}
	bad := map[string]model.Schedule{
		"unknown tz": mk(func(s *model.Schedule) { s.Timezone = "Mars/Olympus" }),
		"empty tz":   mk(func(s *model.Schedule) { s.Timezone = "" }),
		"bad start":  mk(func(s *model.Schedule) { s.StartTime = "9am" }),
		"bad end":    mk(func(s *model.Schedule) { s.EndTime = "25:00" }),
		"bad from":   mk(func(s *model.Schedule) { s.ValidFrom = ptr("June 1") }),
		"bad until":  mk(func(s *model.Schedule) { s.ValidUntil = ptr("2026-13-01") }),
	}
	for name, s := range bad {
		if _, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, At: utc("2026-06-01 00:00")}); !errors.Is(err, model.ErrInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func TestNextChangeAcrossSources(t *testing.T) {
	scheds := []model.Schedule{
		oneOff(1, "2026-06-03 00:00", "2026-06-04 00:00"),
		oneOff(2, "2026-06-01 20:00", "2026-06-01 22:00"),
		recurring(3, "UTC", model.WeekdaysOf(time.Monday), "09:00", "17:00"),
	}
	// 2026-06-01 is a Monday.
	tests := []struct {
		at, next string
		frozen   bool
	}{
		{"2026-06-01 08:00", "2026-06-01 09:00", false},
		{"2026-06-01 10:00", "2026-06-01 17:00", true},
		{"2026-06-01 18:00", "2026-06-01 20:00", false},
		{"2026-06-01 21:00", "2026-06-01 22:00", true},
		{"2026-06-02 00:00", "2026-06-03 00:00", false},
		{"2026-06-03 12:00", "2026-06-04 00:00", true},
		{"2026-06-04 00:00", "2026-06-08 09:00", false},
	}
	for _, tc := range tests {
		d, err := New().Evaluate(Input{Repo: baseRepo(), Schedules: scheds, At: utc(tc.at)})
		if err != nil {
			t.Fatal(err)
		}
		if d.Frozen != tc.frozen {
			t.Errorf("%s frozen=%v", tc.at, d.Frozen)
		}
		checkNext(t, tc.at, d.NextChange, tc.next)
	}
	// Re-evaluating exactly at NextChange must observe the change.
	d, _ := New().Evaluate(Input{Repo: baseRepo(), Schedules: scheds, At: utc("2026-06-01 08:00")})
	d2, _ := New().Evaluate(Input{Repo: baseRepo(), Schedules: scheds, At: *d.NextChange})
	if d2.Frozen == d.Frozen {
		t.Error("state did not change at NextChange")
	}
}

func TestNextChangeHelper(t *testing.T) {
	s := oneOff(1, "2026-06-01 10:00", "2026-06-01 12:00")
	for _, tc := range []struct {
		at   string
		next string
	}{{"2026-06-01 00:00", "2026-06-01 10:00"}, {"2026-06-01 10:00", "2026-06-01 12:00"}, {"2026-06-01 12:00", ""}} {
		got, ok, err := NextChange(s, utc(tc.at))
		if err != nil {
			t.Fatal(err)
		}
		if tc.next == "" && ok || tc.next != "" && (!ok || !got.Equal(utc(tc.next))) {
			t.Errorf("%s: got %v %v want %q", tc.at, got, ok, tc.next)
		}
	}
	if _, _, err := NextChange(model.Schedule{Kind: "x"}, time.Now()); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("err=%v", err)
	}
}

func TestBypass(t *testing.T) {
	org := manual(1, model.FreezeScopeOrg)
	byp := func(pr int) *model.Bypass {
		return &model.Bypass{ID: 9, RepoID: repoID, PRNumber: pr, Reason: "hotfix"}
	}
	ended := byp(5)
	ended.EndedAt = ptr(utc("2026-01-01 00:00"))
	otherRepo := byp(5)
	otherRepo.RepoID = 555

	tests := []struct {
		name     string
		frozen   bool
		pr       int
		bypass   *model.Bypass
		bypassed bool
		blocked  bool
	}{
		{"matching pr bypassed", true, 5, byp(5), true, false},
		{"different pr still blocked", true, 6, byp(5), false, true},
		{"repo-level question ignores bypass", true, 0, byp(5), false, true},
		{"no bypass", true, 5, nil, false, true},
		{"ended bypass ignored", true, 5, ended, false, true},
		{"bypass for another repo ignored", true, 5, otherRepo, false, true},
		{"bypass irrelevant when not frozen", false, 5, byp(5), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Repo: baseRepo(), PR: tc.pr, Bypass: tc.bypass, At: utc("2026-06-01 12:00")}
			if tc.frozen {
				in.OrgFreeze = org
			}
			d, err := New().Evaluate(in)
			if err != nil {
				t.Fatal(err)
			}
			if d.Frozen != tc.frozen || d.Bypassed != tc.bypassed || d.Blocked != tc.blocked {
				t.Fatalf("frozen=%v bypassed=%v blocked=%v", d.Frozen, d.Bypassed, d.Blocked)
			}
			if tc.bypassed && d.BypassID != 9 || !tc.bypassed && d.BypassID != 0 {
				t.Errorf("BypassID=%d", d.BypassID)
			}
		})
	}
}

func TestBypassSurvivesPushAndWindowEnd(t *testing.T) {
	// Bypass is keyed by PR, not commit: evaluation has no SHA input, so the
	// same bypass keeps applying across pushes while frozen, and once the
	// window ends the repo is simply not frozen (callers then end the bypass).
	s := oneOff(1, "2026-06-01 10:00", "2026-06-01 12:00")
	b := &model.Bypass{ID: 3, RepoID: repoID, PRNumber: 8, Reason: "x"}
	for _, at := range []string{"2026-06-01 10:00", "2026-06-01 11:59"} {
		d, _ := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, PR: 8, Bypass: b, At: utc(at)})
		if !d.Bypassed || d.Blocked {
			t.Errorf("%s: %+v", at, d)
		}
	}
	d, _ := New().Evaluate(Input{Repo: baseRepo(), Schedules: []model.Schedule{s}, PR: 8, Bypass: b, At: utc("2026-06-01 12:00")})
	if d.Frozen || d.Bypassed || d.Blocked {
		t.Errorf("after window: %+v", d)
	}
}

func TestEvaluateDoesNotMutateInput(t *testing.T) {
	s := recurring(1, "UTC", model.WeekdaysOf(time.Monday), "09:00", "17:00")
	scheds := []model.Schedule{s}
	in := Input{Repo: baseRepo(), Schedules: scheds, At: utc("2026-06-01 10:00")}
	if _, err := New().Evaluate(in); err != nil {
		t.Fatal(err)
	}
	if scheds[0].StartTime != "09:00" || !in.At.Equal(utc("2026-06-01 10:00")) {
		t.Error("input mutated")
	}
}
