package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// refreshSchedule recomputes, for every active repo, the next instant its
// freeze state can change by itself (a schedule window starting or ending)
// and arms one timer for the earliest. It runs on the run-loop goroutine,
// never inside a clock callback. Without an Engine it does nothing.
func (r *Reconciler) refreshSchedule(ctx context.Context) {
	if r.cfg.Engine == nil {
		return
	}
	next, err := r.computeNextChanges(ctx)
	if err != nil {
		r.cfg.Logger.Error("reconcile: compute schedule boundaries", "err", err)
		return
	}
	var earliest time.Time
	for _, t := range next {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if r.schedTimer != nil {
		r.schedTimer.Stop()
		r.schedTimer = nil
	}
	r.schedNext = next
	if earliest.IsZero() {
		return
	}
	d := earliest.Sub(r.cfg.Clock.Now())
	if d < 0 {
		d = 0
	}
	r.schedTimer = r.cfg.Clock.AfterFunc(d, func() { r.scheduleFired(earliest) })
}

// scheduleFired runs on a clock goroutine: it only records intent.
func (r *Reconciler) scheduleFired(at time.Time) {
	r.mu.Lock()
	for id, t := range r.schedNext {
		if !t.After(at) {
			r.repos[id] = struct{}{}
		}
	}
	r.schedTimer = nil
	r.mu.Unlock()
	r.signal() // the loop expands the repos, then refreshes and re-arms
}

// computeNextChanges returns repoID -> next boundary for every active repo
// that has one.
func (r *Reconciler) computeNextChanges(ctx context.Context) (map[int64]time.Time, error) {
	st := r.cfg.Store
	now := r.cfg.Clock.Now()
	insts, err := st.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]time.Time{}
	for _, in := range insts {
		if in.SuspendedAt != nil {
			continue
		}
		var orgFreeze *model.Freeze
		if f, err := st.ActiveOrgFreeze(ctx, in.ID); err == nil {
			orgFreeze = &f
		} else if !errors.Is(err, model.ErrNotFound) {
			return nil, err
		}
		scheds, err := st.ListSchedules(ctx, store.ScheduleFilter{InstallationID: in.ID, EnabledOnly: true})
		if err != nil {
			return nil, err
		}
		if len(scheds) == 0 {
			continue // nothing can change by itself
		}
		repos, err := st.ListRepos(ctx, store.RepoFilter{InstallationID: in.ID})
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			if repo.RemovedAt != nil || repo.Archived {
				continue
			}
			input := engine.Input{Repo: repo, At: now, OrgFreeze: orgFreeze}
			if f, err := st.ActiveRepoFreeze(ctx, repo.ID); err == nil {
				input.RepoFreeze = &f
			} else if !errors.Is(err, model.ErrNotFound) {
				return nil, err
			}
			for _, s := range scheds {
				if s.RepoID == nil || *s.RepoID == repo.ID {
					input.Schedules = append(input.Schedules, s)
				}
			}
			d, err := r.cfg.Engine.Evaluate(input)
			if err != nil {
				r.cfg.Logger.Warn("reconcile: evaluate repo for schedule", "repo", repo.ID, "err", err)
				continue
			}
			if d.NextChange != nil && d.NextChange.After(now) {
				out[repo.ID] = *d.NextChange
			}
		}
	}
	return out, nil
}
