package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type repoSummaryDTO struct {
	ID             int64     `json:"id"`
	Owner          string    `json:"owner"`
	Name           string    `json:"name"`
	FullName       string    `json:"full_name"`
	InstallationID int64     `json:"installation_id"`
	Private        bool      `json:"private"`
	Archived       bool      `json:"archived"`
	State          stateDTO  `json:"state"`
	OpenPRs        int       `json:"open_prs"`
	BlockedPRs     int       `json:"blocked_prs"`
	PublicStatus   bool      `json:"public_status"`
	Permissions    *permsDTO `json:"permissions,omitempty"`
}

type repoDetailDTO struct {
	repoSummaryDTO
	DefaultBranch string        `json:"default_branch,omitempty"`
	Schedules     []scheduleDTO `json:"schedules"`
	Bypasses      []bypassDTO   `json:"bypasses"`
}

func (s *Server) summarize(ctx context.Context, st store.Store, rc repoCtx) (repoSummaryDTO, error) {
	_, state, err := s.repoState(ctx, st, rc.repo)
	if err != nil {
		return repoSummaryDTO{}, err
	}
	prs, err := st.ListOpenPullRequests(ctx, rc.repo.ID)
	if err != nil {
		return repoSummaryDTO{}, err
	}
	blocked := 0
	for _, p := range prs {
		if p.Conclusion == "failure" {
			blocked++
		}
	}
	r := rc.repo
	return repoSummaryDTO{
		ID: r.ID, Owner: r.Owner, Name: r.Name, FullName: fullName(r), InstallationID: r.InstallationID,
		Private: r.Private, Archived: r.Archived, State: state, OpenPRs: len(prs), BlockedPRs: blocked,
		PublicStatus: r.PublicStatus, Permissions: repoPerms(rc.role),
	}, nil
}

func (s *Server) listRepos(c *call) error {
	ctx := c.r.Context()
	limit, offset, err := c.paging()
	if err != nil {
		return err
	}
	q := c.r.URL.Query()
	stateFilter := q.Get("state")
	if stateFilter != "" && stateFilter != "frozen" && stateFilter != "thawed" {
		return errBadRequest("invalid state", map[string]string{"state": "must be frozen or thawed"})
	}
	all, err := s.store.ListInstallations(ctx)
	if err != nil {
		return err
	}
	insts := map[int64]model.Installation{}
	for _, i := range all {
		if org := q.Get("org"); org == "" || strings.EqualFold(org, i.AccountLogin) {
			insts[i.ID] = i
		}
	}
	items := []repoSummaryDTO{}
	if len(insts) == 0 {
		return c.json(http.StatusOK, map[string]any{"items": items, "total": 0})
	}
	repos, err := s.store.ListRepos(ctx, store.RepoFilter{Query: q.Get("q")})
	if err != nil {
		return err
	}
	inScope := repos[:0]
	for _, r := range repos {
		if _, ok := insts[r.InstallationID]; ok {
			inScope = append(inScope, r)
		}
	}
	sortedRepos(inScope)
	roles, err := s.repoRoles(ctx, c.actor, inScope, insts)
	if err != nil {
		return err
	}
	var matched []repoSummaryDTO
	for _, r := range inScope {
		role := roles[r.ID]
		if role < RoleMember {
			continue
		}
		sum, err := s.summarize(ctx, s.store, repoCtx{repo: r, inst: insts[r.InstallationID], role: role})
		if err != nil {
			return err
		}
		if (stateFilter == "frozen" && !sum.State.Frozen) || (stateFilter == "thawed" && sum.State.Frozen) {
			continue
		}
		matched = append(matched, sum)
	}
	total := len(matched)
	if offset < total {
		items = matched[offset:min(offset+limit, total)]
	}
	return c.json(http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) getRepo(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMember)
	if err != nil {
		return err
	}
	sum, err := s.summarize(ctx, s.store, rc)
	if err != nil {
		return err
	}
	id := rc.repo.ID
	scheds, err := s.store.ListSchedules(ctx, store.ScheduleFilter{InstallationID: rc.inst.ID, AppliesToRepoID: &id})
	if err != nil {
		return err
	}
	bps, err := s.store.ListBypasses(ctx, store.BypassFilter{RepoID: &id, ActiveOnly: true})
	if err != nil {
		return err
	}
	out := repoDetailDTO{repoSummaryDTO: sum, DefaultBranch: rc.repo.DefaultBranch, Schedules: []scheduleDTO{}, Bypasses: []bypassDTO{}}
	for _, sc := range scheds {
		out.Schedules = append(out.Schedules, s.scheduleDTO(sc, rc.inst, &rc.repo))
	}
	for _, b := range bps {
		out.Bypasses = append(out.Bypasses, toBypassDTO(b, fullName(rc.repo)))
	}
	return c.json(http.StatusOK, out)
}

type freezeRequest struct {
	Reason string `json:"reason"`
}

type freezeResultDTO struct {
	State      stateDTO `json:"state"`
	Rechecking int      `json:"rechecking"`
}

func (s *Server) freezeRepo(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	var req freezeRequest
	if err := c.decodeOptional(&req); err != nil {
		return err
	}
	if err := mustReason(req.Reason); err != nil {
		return err
	}
	var res freezeResultDTO
	err = s.store.WithTx(ctx, func(st store.Store) error {
		rid := rc.repo.ID
		_, err := st.CreateFreeze(ctx, model.Freeze{
			Scope: model.FreezeScopeRepo, InstallationID: rc.inst.ID, RepoID: &rid, Reason: strings.TrimSpace(req.Reason),
			CreatedBy: c.actor.User.Login, CreatedByID: c.actor.User.ID, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: rc.inst.ID, RepoID: &rid, Action: model.AuditFreeze, Scope: model.FreezeScopeRepo, Reason: strings.TrimSpace(req.Reason),
		}); err != nil {
			return err
		}
		_, res.State, err = s.repoState(ctx, st, rc.repo)
		res.Rechecking = countOpenPRs(ctx, st, []model.Repo{rc.repo})
		return err
	})
	if err != nil {
		if isConflict(err) {
			return errConflict("repo is already frozen")
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: rc.inst.ID, RepoID: &rc.repo.ID, Action: model.AuditFreeze}})
	return c.json(http.StatusCreated, res)
}

func (s *Server) thawRepo(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	var res freezeResultDTO
	err = s.store.WithTx(ctx, func(st store.Store) error {
		rid := rc.repo.ID
		f, err := st.ActiveRepoFreeze(ctx, rid) // ErrNotFound -> 404
		if err != nil {
			return err
		}
		if _, err := st.EndFreeze(ctx, f.ID, store.ThawInfo{By: c.actor.User.Login, ByID: c.actor.User.ID, At: s.now()}); err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: rc.inst.ID, RepoID: &rid, Action: model.AuditThaw, Scope: model.FreezeScopeRepo,
		}); err != nil {
			return err
		}
		if err := s.settle(ctx, st, c.actor, []model.Repo{rc.repo}); err != nil {
			return err
		}
		_, res.State, err = s.repoState(ctx, st, rc.repo)
		res.Rechecking = countOpenPRs(ctx, st, []model.Repo{rc.repo})
		return err
	})
	if err != nil {
		if isConflict(err) { // raced with another thaw
			return errNotFound()
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: rc.inst.ID, RepoID: &rc.repo.ID, Action: model.AuditThaw}})
	return c.json(http.StatusOK, res)
}

func (s *Server) setPublicStatus(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.decode(&req); err != nil {
		return err
	}
	if req.Enabled == nil {
		return errBadRequest("enabled is required", map[string]string{"enabled": "required"})
	}
	err = s.store.WithTx(ctx, func(st store.Store) error {
		if err := st.SetPublicStatus(ctx, rc.repo.ID, *req.Enabled); err != nil {
			return err
		}
		rid := rc.repo.ID
		d := `{"enabled":false}`
		if *req.Enabled {
			d = `{"enabled":true}`
		}
		return s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: rc.inst.ID, RepoID: &rid, Action: model.AuditPublicStatusSet, Details: d,
		})
	})
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}
