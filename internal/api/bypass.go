package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

type bypassDTO struct {
	ID        int64      `json:"id"`
	Repo      string     `json:"repo"`
	PRNumber  int        `json:"pr_number"`
	Reason    string     `json:"reason"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	EndReason string     `json:"end_reason,omitempty"`
}

func toBypassDTO(b model.Bypass, repo string) bypassDTO {
	return bypassDTO{
		ID: b.ID, Repo: repo, PRNumber: b.PRNumber, Reason: b.Reason, CreatedBy: b.CreatedBy,
		CreatedAt: ts(b.CreatedAt), EndedAt: tsp(b.EndedAt), EndReason: string(b.EndReason),
	}
}

func (s *Server) listBypasses(c *call) error {
	rc, err := s.loadRepo(c, RoleMember)
	if err != nil {
		return err
	}
	active := true
	if v := c.r.URL.Query().Get("active"); v != "" {
		switch v {
		case "true":
		case "false":
			active = false
		default:
			return errBadRequest("invalid active", map[string]string{"active": "must be true or false"})
		}
	}
	limit, _, err := c.paging()
	if err != nil {
		return err
	}
	id := rc.repo.ID
	list, err := s.store.ListBypasses(c.r.Context(), store.BypassFilter{RepoID: &id, ActiveOnly: active, Limit: limit})
	if err != nil {
		return err
	}
	items := []bypassDTO{}
	for _, b := range list {
		items = append(items, toBypassDTO(b, fullName(rc.repo)))
	}
	return c.json(http.StatusOK, map[string]any{"items": items})
}

func prNumber(c *call) (int, error) {
	n, err := c.pathInt("number")
	return int(n), err
}

func (s *Server) createBypass(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	pr, err := prNumber(c)
	if err != nil {
		return err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.decode(&req); err != nil {
		return err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return errBadRequest("a reason is required", map[string]string{"reason": "required"})
	}
	if len(reason) > 500 {
		return errBadRequest("reason is too long", map[string]string{"reason": "must be at most 500 characters"})
	}
	var created model.Bypass
	err = s.store.WithTx(ctx, func(st store.Store) error {
		var err error
		created, err = st.CreateBypass(ctx, model.Bypass{
			RepoID: rc.repo.ID, PRNumber: pr, Reason: reason,
			CreatedBy: c.actor.User.Login, CreatedByID: c.actor.User.ID, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		rid := rc.repo.ID
		return s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: rc.inst.ID, RepoID: &rid, Action: model.AuditBypassCreate, PRNumber: &pr, Reason: reason,
		})
	})
	if err != nil {
		if isConflict(err) {
			return errConflict("this pull request already has an active bypass")
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: rc.inst.ID, RepoID: &rc.repo.ID, PRNumber: pr, Action: model.AuditBypassCreate, Actor: c.actor.User.Login, Reason: reason, Scope: model.FreezeScopeRepo}})
	return c.json(http.StatusCreated, toBypassDTO(created, fullName(rc.repo)))
}

func (s *Server) deleteBypass(c *call) error {
	ctx := c.r.Context()
	rc, err := s.loadRepo(c, RoleMaintainer)
	if err != nil {
		return err
	}
	pr, err := prNumber(c)
	if err != nil {
		return err
	}
	err = s.store.WithTx(ctx, func(st store.Store) error {
		b, err := st.ActiveBypass(ctx, rc.repo.ID, pr)
		if err != nil {
			return err
		}
		if _, err := st.EndBypass(ctx, b.ID, model.BypassEndedRevoked, s.now()); err != nil {
			return err
		}
		rid := rc.repo.ID
		return s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: rc.inst.ID, RepoID: &rid, Action: model.AuditBypassEnd, PRNumber: &pr,
			Details: `{"end_reason":"revoked"}`,
		})
	})
	if err != nil {
		if isConflict(err) {
			return errNotFound()
		}
		return err
	}
	s.emit(ctx, pendingEvents{{InstallationID: rc.inst.ID, RepoID: &rc.repo.ID, PRNumber: pr, Action: model.AuditBypassEnd, Actor: c.actor.User.Login, Scope: model.FreezeScopeRepo}})
	return c.noContent()
}
