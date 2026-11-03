package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

var weekdayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

const dateLayout = "2006-01-02"

type scheduleInput struct {
	Name       string     `json:"name"`
	Reason     string     `json:"reason"`
	Kind       string     `json:"kind"`
	Timezone   string     `json:"timezone"`
	Enabled    *bool      `json:"enabled"`
	StartsAt   *time.Time `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at"`
	Weekdays   []string   `json:"weekdays"`
	StartTime  string     `json:"start_time"`
	EndTime    string     `json:"end_time"`
	ValidFrom  *string    `json:"valid_from"`
	ValidUntil *string    `json:"valid_until"`
}

type scheduleDTO struct {
	ID         int64      `json:"id"`
	Scope      string     `json:"scope"`
	Repo       string     `json:"repo,omitempty"`
	Name       string     `json:"name,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Kind       string     `json:"kind"`
	Timezone   string     `json:"timezone"`
	Enabled    bool       `json:"enabled"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	Weekdays   []string   `json:"weekdays,omitempty"`
	StartTime  string     `json:"start_time,omitempty"`
	EndTime    string     `json:"end_time,omitempty"`
	ValidFrom  *string    `json:"valid_from,omitempty"`
	ValidUntil *string    `json:"valid_until,omitempty"`
	Active     bool       `json:"active"`
	NextStart  *time.Time `json:"next_start,omitempty"`
	NextEnd    *time.Time `json:"next_end,omitempty"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// scheduleFromInput validates in and builds the model value (scope fields
// and creator are filled by the caller).
func scheduleFromInput(in scheduleInput) (model.Schedule, map[string]string) {
	f := map[string]string{}
	sc := model.Schedule{Name: strings.TrimSpace(in.Name), Reason: strings.TrimSpace(in.Reason), Timezone: in.Timezone, Enabled: true}
	if in.Enabled != nil {
		sc.Enabled = *in.Enabled
	}
	if len(in.Name) > 100 {
		f["name"] = "must be at most 100 characters"
	}
	if len(in.Reason) > 500 {
		f["reason"] = "must be at most 500 characters"
	}
	if in.Timezone == "" {
		f["timezone"] = "required"
	} else if _, err := time.LoadLocation(in.Timezone); err != nil {
		f["timezone"] = "unknown IANA timezone"
	}
	switch model.ScheduleKind(in.Kind) {
	case model.ScheduleOneOff:
		sc.Kind = model.ScheduleOneOff
		switch {
		case in.StartsAt == nil:
			f["starts_at"] = "required for one_off"
		case in.EndsAt == nil:
			f["ends_at"] = "required for one_off"
		case !in.StartsAt.Before(*in.EndsAt):
			f["ends_at"] = "must be after starts_at"
		default:
			sc.StartsAt, sc.EndsAt = tsp(in.StartsAt), tsp(in.EndsAt)
		}
	case model.ScheduleRecurring:
		sc.Kind = model.ScheduleRecurring
		if len(in.Weekdays) == 0 {
			f["weekdays"] = "at least one weekday required for recurring"
		}
		for _, d := range in.Weekdays {
			idx := -1
			for i, n := range weekdayNames {
				if n == d {
					idx = i
				}
			}
			switch {
			case idx < 0:
				f["weekdays"] = "must be any of sun, mon, tue, wed, thu, fri, sat"
			case sc.Weekdays.Has(time.Weekday(idx)):
				f["weekdays"] = "must not contain duplicates"
			default:
				sc.Weekdays |= model.WeekdaysOf(time.Weekday(idx))
			}
		}
		if !hhmm.MatchString(in.StartTime) {
			f["start_time"] = "required, HH:MM"
		}
		if !hhmm.MatchString(in.EndTime) {
			f["end_time"] = "required, HH:MM"
		}
		sc.StartTime, sc.EndTime = in.StartTime, in.EndTime
		var from, until time.Time
		var err error
		if in.ValidFrom != nil {
			if from, err = time.Parse(dateLayout, *in.ValidFrom); err != nil {
				f["valid_from"] = "must be YYYY-MM-DD"
			}
			sc.ValidFrom = in.ValidFrom
		}
		if in.ValidUntil != nil {
			if until, err = time.Parse(dateLayout, *in.ValidUntil); err != nil {
				f["valid_until"] = "must be YYYY-MM-DD"
			}
			sc.ValidUntil = in.ValidUntil
		}
		if in.ValidFrom != nil && in.ValidUntil != nil && f["valid_from"] == "" && f["valid_until"] == "" && until.Before(from) {
			f["valid_until"] = "must not be before valid_from"
		}
	default:
		f["kind"] = "must be one_off or recurring"
	}
	if len(f) > 0 {
		return model.Schedule{}, f
	}
	return sc, nil
}

func (s *Server) scheduleDTO(sc model.Schedule, inst model.Installation, repo *model.Repo) scheduleDTO {
	d := scheduleDTO{
		ID: sc.ID, Scope: "org", Name: sc.Name, Reason: sc.Reason, Kind: string(sc.Kind), Timezone: sc.Timezone,
		Enabled: sc.Enabled, StartsAt: tsp(sc.StartsAt), EndsAt: tsp(sc.EndsAt), StartTime: sc.StartTime, EndTime: sc.EndTime,
		ValidFrom: sc.ValidFrom, ValidUntil: sc.ValidUntil, CreatedBy: sc.CreatedBy, CreatedAt: ts(sc.CreatedAt), UpdatedAt: ts(sc.UpdatedAt),
	}
	if sc.RepoID != nil {
		d.Scope = "repo"
		if repo != nil {
			d.Repo = fullName(*repo)
		}
	}
	for i, n := range weekdayNames {
		if sc.Weekdays.Has(time.Weekday(i)) {
			d.Weekdays = append(d.Weekdays, n)
		}
	}
	if sc.Enabled {
		// Evaluate the window alone to learn whether it is active and when
		// it next flips.
		in := engine.Input{Repo: model.Repo{InstallationID: inst.ID}, Schedules: []model.Schedule{sc}, At: s.now()}
		if repo != nil {
			in.Repo = *repo
		}
		if dec, err := s.engine.Evaluate(in); err != nil {
			s.log.Warn("evaluating schedule", "schedule", sc.ID, "err", err)
		} else if dec.Frozen {
			d.Active, d.NextEnd = true, tsp(dec.NextChange)
		} else {
			d.NextStart = tsp(dec.NextChange)
		}
	}
	return d
}

func (s *Server) scheduleList(ctx context.Context, inst model.Installation, repo *model.Repo) ([]scheduleDTO, error) {
	f := store.ScheduleFilter{InstallationID: inst.ID, OrgWideOnly: repo == nil}
	if repo != nil {
		id := repo.ID
		f.RepoID = &id
	}
	list, err := s.store.ListSchedules(ctx, f)
	if err != nil {
		return nil, err
	}
	out := []scheduleDTO{}
	for _, sc := range list {
		out = append(out, s.scheduleDTO(sc, inst, repo))
	}
	return out, nil
}

func (s *Server) listOrgSchedules(c *call) error {
	oc, err := s.loadOrg(c, RoleMember)
	if err != nil {
		return err
	}
	items, err := s.scheduleList(c.r.Context(), oc.inst, nil)
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listRepoSchedules(c *call) error {
	rc, err := s.loadRepo(c, RoleMember)
	if err != nil {
		return err
	}
	items, err := s.scheduleList(c.r.Context(), rc.inst, &rc.repo)
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createOrgSchedule(c *call) error {
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	return s.createSchedule(c, oc.inst, nil)
}

func (s *Server) createRepoSchedule(c *call) error {
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	return s.createSchedule(c, rc.inst, &rc.repo)
}

func (s *Server) createSchedule(c *call, inst model.Installation, repo *model.Repo) error {
	ctx := c.r.Context()
	var in scheduleInput
	if err := c.decode(&in); err != nil {
		return err
	}
	sc, fields := scheduleFromInput(in)
	if fields != nil {
		return errBadRequest("invalid schedule", fields)
	}
	sc.InstallationID = inst.ID
	sc.CreatedBy, sc.CreatedByID = c.actor.User.Login, c.actor.User.ID
	var repoID *int64
	if repo != nil {
		id := repo.ID
		repoID = &id
		sc.RepoID = &id
	}
	var created model.Schedule
	err := s.store.WithTx(ctx, func(st store.Store) error {
		var err error
		if created, err = st.CreateSchedule(ctx, sc); err != nil {
			return err
		}
		return s.audit(ctx, st, c.actor, scheduleAudit(model.AuditScheduleCreate, created))
	})
	if err != nil {
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: inst.ID, RepoID: repoID, Action: model.AuditScheduleCreate}})
	return c.json(http.StatusCreated, s.scheduleDTO(created, inst, repo))
}

func scheduleAudit(action model.AuditAction, sc model.Schedule) model.AuditEntry {
	return model.AuditEntry{
		InstallationID: sc.InstallationID, RepoID: sc.RepoID, Action: action, Reason: sc.Reason,
		Details: jsonObj(map[string]any{"schedule_id": sc.ID, "name": sc.Name, "kind": sc.Kind}),
	}
}

// loadSchedule fetches schedule {id} and authorizes the caller against its
// scope (member for reads, maintainer/owner for writes).
func (s *Server) loadSchedule(c *call, write bool) (model.Schedule, model.Installation, *model.Repo, error) {
	ctx := c.r.Context()
	id, err := c.pathInt("id")
	if err != nil {
		return model.Schedule{}, model.Installation{}, nil, errNotFound()
	}
	sc, err := s.store.GetSchedule(ctx, id)
	if err != nil {
		return sc, model.Installation{}, nil, err
	}
	if sc.RepoID != nil {
		repo, err := s.store.GetRepo(ctx, *sc.RepoID)
		if err != nil {
			return sc, model.Installation{}, nil, err
		}
		need := RoleMember
		if write {
			need = RoleMaintainer
		}
		rc, err := s.authorizeRepo(ctx, c.actor, repo, need)
		return sc, rc.inst, &rc.repo, err
	}
	inst, err := s.store.GetInstallation(ctx, sc.InstallationID)
	if err != nil {
		return sc, inst, nil, err
	}
	need := RoleMember
	if write {
		need = RoleOwner
	}
	oc, err := s.authorizeOrg(ctx, c.actor, inst, need)
	return sc, oc.inst, nil, err
}

func (s *Server) getSchedule(c *call) error {
	sc, inst, repo, err := s.loadSchedule(c, false)
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, s.scheduleDTO(sc, inst, repo))
}

func (s *Server) updateSchedule(c *call) error {
	ctx := c.r.Context()
	old, inst, repo, err := s.loadSchedule(c, true)
	if err != nil {
		return err
	}
	var in scheduleInput
	if err := c.decode(&in); err != nil {
		return err
	}
	next, fields := scheduleFromInput(in)
	if fields != nil {
		return errBadRequest("invalid schedule", fields)
	}
	// Scope, ownership and creation metadata do not change.
	next.ID, next.InstallationID, next.RepoID = old.ID, old.InstallationID, old.RepoID
	next.CreatedBy, next.CreatedByID, next.CreatedAt = old.CreatedBy, old.CreatedByID, old.CreatedAt
	var updated model.Schedule
	err = s.store.WithTx(ctx, func(st store.Store) error {
		var err error
		if updated, err = st.UpdateSchedule(ctx, next); err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, scheduleAudit(model.AuditScheduleUpdate, updated)); err != nil {
			return err
		}
		return s.settleScope(ctx, st, c.actor, updated)
	})
	if err != nil {
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: updated.InstallationID, RepoID: updated.RepoID, Action: model.AuditScheduleUpdate}})
	return c.json(http.StatusOK, s.scheduleDTO(updated, inst, repo))
}

func (s *Server) deleteSchedule(c *call) error {
	ctx := c.r.Context()
	sc, _, _, err := s.loadSchedule(c, true)
	if err != nil {
		return err
	}
	err = s.store.WithTx(ctx, func(st store.Store) error {
		if err := st.DeleteSchedule(ctx, sc.ID); err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, scheduleAudit(model.AuditScheduleDelete, sc)); err != nil {
			return err
		}
		return s.settleScope(ctx, st, c.actor, sc)
	})
	if err != nil {
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: sc.InstallationID, RepoID: sc.RepoID, Action: model.AuditScheduleDelete}})
	return c.noContent()
}

// settleScope ends bypasses of repos a schedule change left unfrozen.
func (s *Server) settleScope(ctx context.Context, st store.Store, a *Actor, sc model.Schedule) error {
	repos, err := affectedRepos(ctx, st, sc.InstallationID, sc.RepoID)
	if err != nil {
		return err
	}
	return s.settle(ctx, st, a, repos)
}
