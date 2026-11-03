package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type orgDTO struct {
	InstallationID int64     `json:"installation_id"`
	Login          string    `json:"login"`
	Type           string    `json:"type,omitempty"`
	AvatarURL      string    `json:"avatar_url,omitempty"`
	RepoCount      int       `json:"repo_count"`
	State          stateDTO  `json:"state"`
	Permissions    *permsDTO `json:"permissions,omitempty"`
}

func (s *Server) orgDTO(ctx context.Context, st store.Store, oc orgCtx) (orgDTO, error) {
	state, err := s.orgState(ctx, st, oc.inst.ID)
	if err != nil {
		return orgDTO{}, err
	}
	repos, err := st.ListRepos(ctx, store.RepoFilter{InstallationID: oc.inst.ID})
	if err != nil {
		return orgDTO{}, err
	}
	return orgDTO{
		InstallationID: oc.inst.ID, Login: oc.inst.AccountLogin, Type: oc.inst.AccountType,
		AvatarURL: "https://github.com/" + oc.inst.AccountLogin + ".png", RepoCount: len(repos),
		State: state, Permissions: orgPerms(oc.role),
	}, nil
}

func (s *Server) listOrgs(c *call) error {
	ctx := c.r.Context()
	all, err := s.store.ListInstallations(ctx)
	if err != nil {
		return err
	}
	items := []orgDTO{}
	for _, inst := range all {
		role, err := s.orgRole(ctx, c.actor, inst)
		if err != nil {
			return err
		}
		if role < RoleMember {
			continue
		}
		d, err := s.orgDTO(ctx, s.store, orgCtx{inst: inst, role: role})
		if err != nil {
			return err
		}
		items = append(items, d)
	}
	return c.json(http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getOrg(c *call) error {
	oc, err := s.loadOrg(c, RoleMember)
	if err != nil {
		return err
	}
	d, err := s.orgDTO(c.r.Context(), s.store, oc)
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, d)
}

func (s *Server) freezeOrg(c *call) error {
	ctx := c.r.Context()
	oc, err := s.loadOrg(c, RoleOwner)
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
	reason := strings.TrimSpace(req.Reason)
	var out orgFreezeResultDTO
	err = s.store.WithTx(ctx, func(st store.Store) error {
		if _, err := st.CreateFreeze(ctx, model.Freeze{
			Scope: model.FreezeScopeOrg, InstallationID: oc.inst.ID, Reason: reason,
			CreatedBy: c.actor.User.Login, CreatedByID: c.actor.User.ID, CreatedAt: s.now(),
		}); err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: oc.inst.ID, Action: model.AuditFreeze, Scope: model.FreezeScopeOrg, Reason: reason,
		}); err != nil {
			return err
		}
		return s.fillOrgResult(ctx, st, oc, &out)
	})
	if err != nil {
		if isConflict(err) {
			return errConflict("org is already frozen")
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: oc.inst.ID, Action: model.AuditFreeze}})
	return c.json(http.StatusCreated, out)
}

func (s *Server) thawOrg(c *call) error {
	ctx := c.r.Context()
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	var out orgFreezeResultDTO
	err = s.store.WithTx(ctx, func(st store.Store) error {
		f, err := st.ActiveOrgFreeze(ctx, oc.inst.ID)
		if err != nil {
			return err
		}
		if _, err := st.EndFreeze(ctx, f.ID, store.ThawInfo{By: c.actor.User.Login, ByID: c.actor.User.ID, At: s.now()}); err != nil {
			return err
		}
		if err := s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: oc.inst.ID, Action: model.AuditThaw, Scope: model.FreezeScopeOrg,
		}); err != nil {
			return err
		}
		repos, err := affectedRepos(ctx, st, oc.inst.ID, nil)
		if err != nil {
			return err
		}
		if err := s.settle(ctx, st, c.actor, repos); err != nil {
			return err
		}
		return s.fillOrgResult(ctx, st, oc, &out)
	})
	if err != nil {
		if isConflict(err) {
			return errNotFound()
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: oc.inst.ID, Action: model.AuditThaw}})
	return c.json(http.StatusOK, out)
}

type orgFreezeResultDTO struct {
	Org        orgDTO `json:"org"`
	Rechecking int    `json:"rechecking"`
}

func (s *Server) fillOrgResult(ctx context.Context, st store.Store, oc orgCtx, out *orgFreezeResultDTO) error {
	d, err := s.orgDTO(ctx, st, oc)
	if err != nil {
		return err
	}
	repos, err := affectedRepos(ctx, st, oc.inst.ID, nil)
	if err != nil {
		return err
	}
	out.Org, out.Rechecking = d, countOpenPRs(ctx, st, repos)
	return nil
}
