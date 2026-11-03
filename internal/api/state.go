package api

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// ---- DTOs ----

type reasonDTO struct {
	Kind       string     `json:"kind"`
	FreezeID   int64      `json:"freeze_id,omitempty"`
	ScheduleID int64      `json:"schedule_id,omitempty"`
	Text       string     `json:"text,omitempty"`
	By         string     `json:"by,omitempty"`
	Since      time.Time  `json:"since"`
	Until      *time.Time `json:"until,omitempty"`
}

type stateDTO struct {
	Frozen     bool        `json:"frozen"`
	Reasons    []reasonDTO `json:"reasons"`
	NextChange *time.Time  `json:"next_change,omitempty"`
}

type permsDTO struct {
	CanFreeze          bool  `json:"can_freeze"`
	CanThaw            bool  `json:"can_thaw"`
	CanBypass          bool  `json:"can_bypass"`
	CanManageSchedules bool  `json:"can_manage_schedules"`
	CanManageSlack     *bool `json:"can_manage_slack,omitempty"`
}

func repoPerms(r Role) *permsDTO {
	m := r >= RoleMaintainer
	return &permsDTO{CanFreeze: m, CanThaw: m, CanBypass: m, CanManageSchedules: m}
}

func orgPerms(r Role) *permsDTO {
	o := r >= RoleOwner
	return &permsDTO{CanFreeze: o, CanThaw: o, CanBypass: o, CanManageSchedules: o, CanManageSlack: &o}
}

// ---- evaluation ----

func notFoundToNil[T any](v T, err error) (*T, error) {
	if errors.Is(err, model.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// repoInput loads everything the engine needs for repo-level evaluation.
func repoInput(ctx context.Context, st store.Store, repo model.Repo, at time.Time) (engine.Input, error) {
	of, err := notFoundToNil(st.ActiveOrgFreeze(ctx, repo.InstallationID))
	if err != nil {
		return engine.Input{}, err
	}
	rf, err := notFoundToNil(st.ActiveRepoFreeze(ctx, repo.ID))
	if err != nil {
		return engine.Input{}, err
	}
	id := repo.ID
	scheds, err := st.ListSchedules(ctx, store.ScheduleFilter{InstallationID: repo.InstallationID, AppliesToRepoID: &id, EnabledOnly: true})
	if err != nil {
		return engine.Input{}, err
	}
	return engine.Input{Repo: repo, OrgFreeze: of, RepoFreeze: rf, Schedules: scheds, At: at}, nil
}

// orgInput is like repoInput but for the org-wide causes only.
func orgInput(ctx context.Context, st store.Store, instID int64, at time.Time) (engine.Input, error) {
	of, err := notFoundToNil(st.ActiveOrgFreeze(ctx, instID))
	if err != nil {
		return engine.Input{}, err
	}
	scheds, err := st.ListSchedules(ctx, store.ScheduleFilter{InstallationID: instID, OrgWideOnly: true, EnabledOnly: true})
	if err != nil {
		return engine.Input{}, err
	}
	return engine.Input{Repo: model.Repo{InstallationID: instID}, OrgFreeze: of, Schedules: scheds, At: at}, nil
}

func (s *Server) evaluate(in engine.Input) (engine.Decision, stateDTO, error) {
	dec, err := s.engine.Evaluate(in)
	if err != nil {
		return dec, stateDTO{}, err
	}
	out := stateDTO{Frozen: dec.Frozen, Reasons: []reasonDTO{}, NextChange: tsp(dec.NextChange)}
	for _, r := range dec.Reasons {
		d := reasonDTO{Kind: string(r.Kind), FreezeID: r.FreezeID, ScheduleID: r.ScheduleID, Text: r.Text, Since: ts(r.Since), Until: tsp(r.Until)}
		for _, f := range []*model.Freeze{in.OrgFreeze, in.RepoFreeze} {
			if f != nil && (r.Kind == engine.SourceOrgFreeze || r.Kind == engine.SourceRepoFreeze) && f.ID == r.FreezeID {
				d.By = f.CreatedBy
			}
		}
		out.Reasons = append(out.Reasons, d)
	}
	return dec, out, nil
}

func (s *Server) repoState(ctx context.Context, st store.Store, repo model.Repo) (engine.Decision, stateDTO, error) {
	in, err := repoInput(ctx, st, repo, s.now())
	if err != nil {
		return engine.Decision{}, stateDTO{}, err
	}
	return s.evaluate(in)
}

func (s *Server) orgState(ctx context.Context, st store.Store, instID int64) (stateDTO, error) {
	in, err := orgInput(ctx, st, instID, s.now())
	if err != nil {
		return stateDTO{}, err
	}
	_, d, err := s.evaluate(in)
	return d, err
}

// ---- lookups & permissions ----

func (s *Server) findInstallation(ctx context.Context, login string) (model.Installation, error) {
	all, err := s.store.ListInstallations(ctx)
	if err != nil {
		return model.Installation{}, err
	}
	for _, i := range all {
		if strings.EqualFold(i.AccountLogin, login) {
			return i, nil
		}
	}
	return model.Installation{}, model.ErrNotFound
}

// repoRole is the user's effective role on a repo: GitHub repo permission,
// raised by org membership (read) or org ownership (full control).
func (s *Server) repoRole(ctx context.Context, a *Actor, repo model.Repo, inst model.Installation) (Role, error) {
	org, err := s.perms.OrgRole(ctx, *a, inst)
	if err != nil {
		return RoleNone, err
	}
	if org >= RoleOwner {
		return RoleOwner, nil
	}
	role := RoleNone
	if org >= RoleMember {
		role = RoleMember
	}
	direct, err := s.perms.RepoRole(ctx, *a, repo)
	if err != nil {
		return RoleNone, err
	}
	return max(role, direct), nil
}

// orgRole is the user's role on an installation. A user with no org
// membership but access to at least one repo is a member.
func (s *Server) orgRole(ctx context.Context, a *Actor, inst model.Installation) (Role, error) {
	org, err := s.perms.OrgRole(ctx, *a, inst)
	if err != nil || org != RoleNone {
		return org, err
	}
	repos, err := s.store.ListRepos(ctx, store.RepoFilter{InstallationID: inst.ID})
	if err != nil {
		return RoleNone, err
	}
	for _, r := range repos {
		d, err := s.perms.RepoRole(ctx, *a, r)
		if err != nil {
			return RoleNone, err
		}
		if d >= RoleMember {
			return RoleMember, nil
		}
	}
	return RoleNone, nil
}

type repoCtx struct {
	repo model.Repo
	inst model.Installation
	role Role
}

// loadRepo resolves {owner}/{repo} for the caller. Unknown, removed or
// invisible repos are all a 404.
func (s *Server) loadRepo(c *call, need Role) (repoCtx, error) {
	ctx := c.r.Context()
	repo, err := s.store.GetRepoByName(ctx, c.r.PathValue("owner"), c.r.PathValue("repo"))
	if err != nil {
		return repoCtx{}, err
	}
	if repo.RemovedAt != nil {
		return repoCtx{}, model.ErrNotFound
	}
	return s.authorizeRepo(ctx, c.actor, repo, need)
}

func (s *Server) authorizeRepo(ctx context.Context, a *Actor, repo model.Repo, need Role) (repoCtx, error) {
	inst, err := s.store.GetInstallation(ctx, repo.InstallationID)
	if err != nil {
		return repoCtx{}, err
	}
	role, err := s.repoRole(ctx, a, repo, inst)
	if err != nil {
		return repoCtx{}, err
	}
	if role < RoleMember {
		return repoCtx{}, model.ErrNotFound
	}
	if role < need {
		return repoCtx{}, errForbidden("insufficient permissions on " + fullName(repo))
	}
	return repoCtx{repo: repo, inst: inst, role: role}, nil
}

type orgCtx struct {
	inst model.Installation
	role Role
}

func (s *Server) loadOrg(c *call, need Role) (orgCtx, error) {
	ctx := c.r.Context()
	inst, err := s.findInstallation(ctx, c.r.PathValue("org"))
	if err != nil {
		return orgCtx{}, err
	}
	return s.authorizeOrg(ctx, c.actor, inst, need)
}

func (s *Server) authorizeOrg(ctx context.Context, a *Actor, inst model.Installation, need Role) (orgCtx, error) {
	role, err := s.orgRole(ctx, a, inst)
	if err != nil {
		return orgCtx{}, err
	}
	if role < RoleMember {
		return orgCtx{}, model.ErrNotFound
	}
	if role < need {
		return orgCtx{}, errForbidden("insufficient permissions on " + inst.AccountLogin)
	}
	return orgCtx{inst: inst, role: role}, nil
}

// repoRoles computes roles for many repos with bounded concurrency. Repos of
// installations the user owns skip per-repo GitHub calls.
func (s *Server) repoRoles(ctx context.Context, a *Actor, repos []model.Repo, insts map[int64]model.Installation) (map[int64]Role, error) {
	roles := make(map[int64]Role, len(repos))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, r := range repos {
		inst, ok := insts[r.InstallationID]
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			role, err := s.repoRole(ctx, a, r, inst)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			roles[r.ID] = role
		}()
	}
	wg.Wait()
	return roles, firstErr
}

// ---- events, audit, bypass settling ----

type pendingEvents []FreezeChange

func (s *Server) emit(ctx context.Context, evs pendingEvents) {
	for _, e := range evs {
		s.notifier.FreezeStateChanged(ctx, e)
	}
}

func (s *Server) audit(ctx context.Context, st store.Store, a *Actor, e model.AuditEntry) error {
	e.ActorLogin, e.ActorID = a.User.Login, a.User.ID
	e.CreatedAt = s.now()
	_, err := st.AppendAudit(ctx, e)
	return err
}

// affectedRepos returns the live repos a change to (instID, repoID) touches.
func affectedRepos(ctx context.Context, st store.Store, instID int64, repoID *int64) ([]model.Repo, error) {
	if repoID != nil {
		r, err := st.GetRepo(ctx, *repoID)
		if err != nil {
			return nil, err
		}
		return []model.Repo{r}, nil
	}
	return st.ListRepos(ctx, store.RepoFilter{InstallationID: instID})
}

// settle ends active bypasses of repos that are no longer frozen at all, and
// audits each one. Runs inside the caller's transaction.
func (s *Server) settle(ctx context.Context, st store.Store, a *Actor, repos []model.Repo) error {
	for _, repo := range repos {
		dec, _, err := s.repoState(ctx, st, repo)
		if err != nil {
			return err
		}
		if dec.Frozen {
			continue
		}
		rid := repo.ID
		active, err := st.ListBypasses(ctx, store.BypassFilter{RepoID: &rid, ActiveOnly: true})
		if err != nil || len(active) == 0 {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := st.EndBypassesForRepo(ctx, repo.ID, model.BypassEndedThawed, s.now()); err != nil {
			return err
		}
		for _, b := range active {
			pr := b.PRNumber
			if err := s.audit(ctx, st, a, model.AuditEntry{
				InstallationID: repo.InstallationID, RepoID: &rid, Action: model.AuditBypassEnd,
				PRNumber: &pr, Details: `{"end_reason":"thawed"}`,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// countOpenPRs counts open tracked PRs in the given repos.
func countOpenPRs(ctx context.Context, st store.Store, repos []model.Repo) int {
	n := 0
	for _, r := range repos {
		prs, err := st.ListOpenPullRequests(ctx, r.ID)
		if err == nil {
			n += len(prs)
		}
	}
	return n
}

func sortedRepos(rs []model.Repo) {
	sort.Slice(rs, func(i, j int) bool {
		return strings.ToLower(fullName(rs[i])) < strings.ToLower(fullName(rs[j]))
	})
}
