package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// Window is a contiguous span during which a schedule freezes a repo. It is
// half-open: [Start, End).
type Window struct {
	Start time.Time
	End   time.Time
	// OpenEnded is true when the schedule is effectively continuous (for
	// example every day, 24h long) and no end was found within the search
	// horizon. End is then only a lower bound.
	OpenEnded bool
}

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

// maxScanDays bounds how far ahead a recurring schedule is searched for the
// end of a chain of back-to-back windows.
const maxScanDays = 400

// NextWindow returns the first window of s that is active at, or begins
// after, at. Back-to-back or overlapping windows of the same schedule are
// merged, so Start and End are real state changes of that schedule. It
// returns nil when the schedule never freezes at or after at.
//
// Callers should not pass disabled schedules; NextWindow does not look at
// Enabled.
//
// DST rules for recurring windows (times are local wall-clock in the
// schedule's timezone):
//   - A local time that does not exist (spring-forward gap) resolves to the
//     instant of the transition. A window whose start and end both fall in
//     the gap is empty and never freezes.
//   - A local time that occurs twice (fall-back overlap) resolves to the
//     first occurrence for a window start and the second for a window end,
//     so the freeze is never shorter than the wall-clock span suggests.
func NextWindow(s model.Schedule, at time.Time) (*Window, error) {
	switch s.Kind {
	case model.ScheduleOneOff:
		return oneOffWindow(s, at)
	case model.ScheduleRecurring:
		return recurringWindow(s, at)
	default:
		return nil, fmt.Errorf("%w: schedule %d: unknown kind %q", model.ErrInvalid, s.ID, s.Kind)
	}
}

// NextChange returns the next instant strictly after at when s switches
// between active and inactive. ok is false if it never changes again.
func NextChange(s model.Schedule, at time.Time) (t time.Time, ok bool, err error) {
	w, err := NextWindow(s, at)
	if err != nil || w == nil {
		return time.Time{}, false, err
	}
	if w.Start.After(at) {
		return w.Start, true, nil
	}
	if w.OpenEnded {
		return time.Time{}, false, nil
	}
	return w.End, true, nil
}

func oneOffWindow(s model.Schedule, at time.Time) (*Window, error) {
	if s.StartsAt == nil || s.EndsAt == nil {
		return nil, fmt.Errorf("%w: schedule %d: one-off needs starts_at and ends_at", model.ErrInvalid, s.ID)
	}
	if !s.EndsAt.After(*s.StartsAt) {
		return nil, fmt.Errorf("%w: schedule %d: ends_at must be after starts_at", model.ErrInvalid, s.ID)
	}
	if !s.EndsAt.After(at) {
		return nil, nil
	}
	return &Window{Start: *s.StartsAt, End: *s.EndsAt}, nil
}

func recurringWindow(s model.Schedule, at time.Time) (*Window, error) {
	invalid := func(format string, a ...any) error {
		return fmt.Errorf("%w: schedule %d: %s", model.ErrInvalid, s.ID, fmt.Sprintf(format, a...))
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Timezone == "" {
		return nil, invalid("unknown timezone %q", s.Timezone)
	}
	st, err := time.Parse("15:04", s.StartTime)
	if err != nil {
		return nil, invalid("bad start_time %q", s.StartTime)
	}
	en, err := time.Parse("15:04", s.EndTime)
	if err != nil {
		return nil, invalid("bad end_time %q", s.EndTime)
	}
	var validFrom, validUntil *time.Time
	if s.ValidFrom != nil {
		d, err := time.Parse("2006-01-02", *s.ValidFrom)
		if err != nil {
			return nil, invalid("bad valid_from %q", *s.ValidFrom)
		}
		validFrom = &d
	}
	if s.ValidUntil != nil {
		d, err := time.Parse("2006-01-02", *s.ValidUntil)
		if err != nil {
			return nil, invalid("bad valid_until %q", *s.ValidUntil)
		}
		validUntil = &d
	}
	if s.Weekdays == 0 {
		return nil, nil
	}
	overnight := s.EndTime <= s.StartTime

	// Local calendar dates as UTC midnights; only Y-M-D and weekday matter.
	l := at.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day()-2, 0, 0, 0, 0, time.UTC)
	if validFrom != nil && validFrom.After(day) {
		day = *validFrom
	}

	var cur *Window
	for i := 0; i < maxScanDays; i, day = i+1, day.AddDate(0, 0, 1) {
		if validUntil != nil && day.After(*validUntil) {
			break
		}
		if !s.Weekdays.Has(day.Weekday()) {
			continue
		}
		endDay := day
		if overnight {
			endDay = day.AddDate(0, 0, 1)
		}
		w := Window{
			Start: resolveLocal(loc, day, st.Hour(), st.Minute(), false),
			End:   resolveLocal(loc, endDay, en.Hour(), en.Minute(), true),
		}
		if !w.End.After(w.Start) {
			continue // empty (both ends fell in a DST gap)
		}
		switch {
		case cur == nil:
			cur = &w
		case !w.Start.After(cur.End):
			if w.End.After(cur.End) {
				cur.End = w.End
			}
		default:
			if cur.End.After(at) {
				return cur, nil
			}
			cur = &w
		}
	}
	if cur == nil || !cur.End.After(at) {
		return nil, nil
	}
	// Reaching here with the scan exhausted by the cap means an unbroken
	// chain; with ValidUntil it is a genuine end.
	if validUntil == nil || !day.After(*validUntil) {
		cur.OpenEnded = true
	}
	return cur, nil
}

// resolveLocal converts a local wall-clock time on the given calendar day
// (a UTC-midnight time.Time carrying Y-M-D) into an instant in loc, applying
// the DST rules documented on NextWindow. preferLater selects the second
// occurrence of an ambiguous time.
func resolveLocal(loc *time.Location, day time.Time, hour, min int, preferLater bool) time.Time {
	y, mo, d := day.Date()
	guess := time.Date(y, mo, d, hour, min, 0, 0, loc)
	wall := time.Date(y, mo, d, hour, min, 0, 0, time.UTC)

	var cands []time.Time
	for _, probe := range []time.Time{guess.Add(-24 * time.Hour), guess, guess.Add(24 * time.Hour)} {
		_, off := probe.Zone()
		c := wall.Add(-time.Duration(off) * time.Second)
		l := c.In(loc)
		if l.Year() != y || l.Month() != mo || l.Day() != d || l.Hour() != hour || l.Minute() != min {
			continue
		}
		dup := false
		for _, e := range cands {
			dup = dup || e.Equal(c)
		}
		if !dup {
			cands = append(cands, c)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	switch {
	case len(cands) == 1:
		return cands[0]
	case len(cands) > 1:
		if preferLater {
			return cands[len(cands)-1]
		}
		return cands[0]
	}
	// Gap: interpret with the pre-transition offset (which lands after the
	// transition) and snap back to the transition instant.
	_, off := guess.Add(-24 * time.Hour).Zone()
	pre := wall.Add(-time.Duration(off) * time.Second)
	if start, _ := pre.In(loc).ZoneBounds(); !start.IsZero() {
		return start
	}
	return pre
}
