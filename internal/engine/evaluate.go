package engine

import (
	"fmt"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

type evaluator struct{}

// Evaluate implements Evaluator. Invalid input (zero At, malformed enabled
// schedule, unknown timezone) yields an error wrapping model.ErrInvalid.
func (evaluator) Evaluate(in Input) (Decision, error) {
	if in.At.IsZero() {
		return Decision{}, fmt.Errorf("%w: engine: At is required", model.ErrInvalid)
	}
	var d Decision

	if f := in.OrgFreeze; f != nil && f.Active() {
		d.Reasons = append(d.Reasons, freezeReason(SourceOrgFreeze, *f))
	}
	if f := in.RepoFreeze; f != nil && f.Active() {
		d.Reasons = append(d.Reasons, freezeReason(SourceRepoFreeze, *f))
	}

	for _, s := range in.Schedules {
		if !s.Enabled || !appliesTo(s, in.Repo) {
			continue
		}
		w, err := NextWindow(s, in.At)
		if err != nil {
			return Decision{}, err
		}
		if w == nil {
			continue
		}
		if w.Start.After(in.At) {
			d.noteChange(w.Start)
			continue
		}
		r := Reason{
			Kind:       SourceSchedule,
			ScheduleID: s.ID,
			Text:       s.Name,
			Since:      w.Start,
		}
		if r.Text == "" {
			r.Text = s.Reason
		}
		if !w.OpenEnded {
			end := w.End
			r.Until = &end
			d.noteChange(end)
		}
		d.Reasons = append(d.Reasons, r)
	}

	d.Frozen = len(d.Reasons) > 0
	if d.Frozen && in.PR != 0 {
		if b := in.Bypass; b != nil && b.Active() && b.PRNumber == in.PR && b.RepoID == in.Repo.ID {
			d.Bypassed = true
			d.BypassID = b.ID
		}
	}
	d.Blocked = d.Frozen && !d.Bypassed
	return d, nil
}

func (d *Decision) noteChange(t time.Time) {
	if d.NextChange == nil || t.Before(*d.NextChange) {
		d.NextChange = &t
	}
}

func freezeReason(kind SourceKind, f model.Freeze) Reason {
	return Reason{Kind: kind, FreezeID: f.ID, Text: f.Reason, Since: f.CreatedAt}
}

// appliesTo reports whether a schedule covers the repo: repo-scoped windows
// by repo ID, org-wide ones by installation.
func appliesTo(s model.Schedule, r model.Repo) bool {
	if s.RepoID != nil {
		return *s.RepoID == r.ID
	}
	return s.InstallationID == r.InstallationID
}
