package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type auditDTO struct {
	ID        int64          `json:"id"`
	Action    string         `json:"action"`
	Org       string         `json:"org"`
	Repo      string         `json:"repo,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	PRNumber  *int           `json:"pr_number,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

var auditActions = map[model.AuditAction]bool{
	model.AuditFreeze: true, model.AuditThaw: true, model.AuditScheduleCreate: true, model.AuditScheduleUpdate: true,
	model.AuditScheduleDelete: true, model.AuditBypassCreate: true, model.AuditBypassEnd: true,
	model.AuditSlackUpdate: true, model.AuditPublicStatusSet: true,
}

func (s *Server) listAudit(c *call) error {
	ctx := c.r.Context()
	q := c.r.URL.Query()
	limit, _, err := c.paging()
	if err != nil {
		return err
	}
	f := store.AuditFilter{Actor: q.Get("actor")}
	if a := model.AuditAction(q.Get("action")); a != "" {
		if !auditActions[a] {
			return errBadRequest("invalid action", map[string]string{"action": "unknown action"})
		}
		f.Action = a
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return errBadRequest("invalid before", map[string]string{"before": "must be a positive integer"})
		}
		f.BeforeID = n
	}
	if org := q.Get("org"); org != "" {
		oc, err := s.authorizeOrgByLogin(ctx, c.actor, org)
		if err != nil {
			return err
		}
		f.InstallationID = oc.inst.ID
	}
	if repoName := q.Get("repo"); repoName != "" {
		owner, name, ok := strings.Cut(repoName, "/")
		if !ok {
			return errBadRequest("invalid repo", map[string]string{"repo": "must be owner/name"})
		}
		repo, err := s.store.GetRepoByName(ctx, owner, name)
		if err != nil {
			return err
		}
		rc, err := s.authorizeRepo(ctx, c.actor, repo, RoleMember)
		if err != nil {
			return err
		}
		f.RepoID = &rc.repo.ID
		f.InstallationID = rc.inst.ID
	}

	vis := &visibility{s: s, a: c.actor, orgs: map[int64]*model.Installation{}, repos: map[int64]*model.Repo{}}
	items := []auditDTO{}
	batch := max(limit, 50)
	for pages := 0; pages < 20 && len(items) < limit; pages++ {
		f.Limit = batch
		entries, err := s.store.ListAudit(ctx, f)
		if err != nil {
			return err
		}
		for _, e := range entries {
			f.BeforeID = e.ID
			dto, ok, err := vis.entry(ctx, e)
			if err != nil {
				return err
			}
			if ok {
				items = append(items, dto)
				if len(items) == limit {
					break
				}
			}
		}
		if len(entries) < batch {
			break
		}
	}
	out := map[string]any{"items": items}
	if len(items) == limit {
		out["next_before"] = items[len(items)-1].ID
	}
	return c.json(http.StatusOK, out)
}

func (s *Server) authorizeOrgByLogin(ctx context.Context, a *Actor, login string) (orgCtx, error) {
	inst, err := s.findInstallation(ctx, login)
	if err != nil {
		return orgCtx{}, err
	}
	return s.authorizeOrg(ctx, a, inst, RoleMember)
}

// visibility memoizes per-request access decisions while filtering audit rows.
type visibility struct {
	s     *Server
	a     *Actor
	orgs  map[int64]*model.Installation // nil value = not visible
	repos map[int64]*model.Repo
}

func (v *visibility) org(ctx context.Context, id int64) (*model.Installation, error) {
	if inst, ok := v.orgs[id]; ok {
		return inst, nil
	}
	inst, err := v.s.store.GetInstallation(ctx, id)
	if err != nil {
		return nil, err
	}
	role, err := v.s.orgRole(ctx, v.a, inst)
	if err != nil {
		return nil, err
	}
	var res *model.Installation
	if role >= RoleMember {
		res = &inst
	}
	v.orgs[id] = res
	return res, nil
}

func (v *visibility) repo(ctx context.Context, id int64, inst model.Installation) (*model.Repo, error) {
	if r, ok := v.repos[id]; ok {
		return r, nil
	}
	repo, err := v.s.store.GetRepo(ctx, id)
	if err != nil {
		return nil, err
	}
	role, err := v.s.repoRole(ctx, v.a, repo, inst)
	if err != nil {
		return nil, err
	}
	var res *model.Repo
	if role >= RoleMember {
		res = &repo
	}
	v.repos[id] = res
	return res, nil
}

func (v *visibility) entry(ctx context.Context, e model.AuditEntry) (auditDTO, bool, error) {
	inst, err := v.s.store.GetInstallation(ctx, e.InstallationID)
	if err != nil {
		return auditDTO{}, false, err
	}
	d := auditDTO{
		ID: e.ID, Action: string(e.Action), Org: inst.AccountLogin, Scope: string(e.Scope), Actor: e.ActorLogin,
		PRNumber: e.PRNumber, Reason: e.Reason, CreatedAt: ts(e.CreatedAt),
	}
	if e.RepoID != nil {
		repo, err := v.repo(ctx, *e.RepoID, inst)
		if err != nil || repo == nil {
			return auditDTO{}, false, err
		}
		d.Repo = fullName(*repo)
	} else {
		o, err := v.org(ctx, e.InstallationID)
		if err != nil || o == nil {
			return auditDTO{}, false, err
		}
	}
	if e.Details != "" {
		var m map[string]any
		if json.Unmarshal([]byte(e.Details), &m) == nil && len(m) > 0 {
			d.Details = m
		}
	}
	return d, true, nil
}
