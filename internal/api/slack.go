package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/slack"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

var slackEventNames = map[string]bool{"freeze": true, "thaw": true, "bypass": true, "schedule": true}

// slackSettingsDTO never carries the webhook URL, only a masked form.
type slackSettingsDTO struct {
	Configured bool     `json:"configured"`
	Enabled    bool     `json:"enabled"`
	Channel    string   `json:"channel,omitempty"`
	Events     []string `json:"events"`
	Masked     string   `json:"webhook_url_masked,omitempty"`
}

func toSlackDTO(cfg model.SlackConfig) slackSettingsDTO {
	d := slackSettingsDTO{Events: []string{}}
	if cfg.WebhookURL == "" {
		return d
	}
	d.Configured, d.Enabled, d.Channel = true, cfg.Enabled, cfg.Channel
	d.Masked = slack.MaskWebhookURL(cfg.WebhookURL)
	d.Events = append(d.Events, cfg.Events...)
	return d
}

func (s *Server) getSlack(c *call) error {
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	cfg, err := s.store.GetSlackConfig(c.r.Context(), oc.inst.ID)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return err
	}
	return c.json(http.StatusOK, toSlackDTO(cfg))
}

type slackInput struct {
	WebhookURL string   `json:"webhook_url"`
	Enabled    bool     `json:"enabled"`
	Channel    string   `json:"channel"`
	Events     []string `json:"events"`
}

func (s *Server) putSlack(c *call) error {
	ctx := c.r.Context()
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	var in slackInput
	if err := c.decode(&in); err != nil {
		return err
	}
	for _, ev := range in.Events {
		if !slackEventNames[ev] {
			return errBadRequest("invalid event", map[string]string{"events": "must be any of freeze, thaw, bypass, schedule"})
		}
	}
	channel := strings.TrimSpace(in.Channel)
	if len(channel) > 100 {
		return errBadRequest("channel is too long", map[string]string{"channel": "must be at most 100 characters"})
	}
	url := strings.TrimSpace(in.WebhookURL)
	var out model.SlackConfig
	err = s.store.WithTx(ctx, func(st store.Store) error {
		if url == "" {
			old, err := st.GetSlackConfig(ctx, oc.inst.ID)
			if errors.Is(err, model.ErrNotFound) || (err == nil && old.WebhookURL == "") {
				return errBadRequest("webhook_url is required", map[string]string{"webhook_url": "required"})
			}
			if err != nil {
				return err
			}
			url = old.WebhookURL
		} else if err := slack.ValidateWebhookURL(url, s.cfg.SlackAllowAnyHost); err != nil {
			return errBadRequest(err.Error(), map[string]string{"webhook_url": err.Error()})
		}
		out = model.SlackConfig{
			InstallationID: oc.inst.ID, WebhookURL: url, Channel: channel, Enabled: in.Enabled,
			Events: in.Events, UpdatedAt: s.now(),
		}
		if err := st.UpsertSlackConfig(ctx, out); err != nil {
			return err
		}
		return s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: oc.inst.ID, Action: model.AuditSlackUpdate,
			Details: jsonObj(map[string]any{
				"enabled": in.Enabled, "channel": channel, "events": nonNil(in.Events),
				"webhook_changed": strings.TrimSpace(in.WebhookURL) != "",
			}),
		})
	})
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, toSlackDTO(out))
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) deleteSlack(c *call) error {
	ctx := c.r.Context()
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	err = s.store.WithTx(ctx, func(st store.Store) error {
		if err := st.DeleteSlackConfig(ctx, oc.inst.ID); err != nil && !errors.Is(err, model.ErrNotFound) {
			return err
		}
		return s.audit(ctx, st, c.actor, model.AuditEntry{
			InstallationID: oc.inst.ID, Action: model.AuditSlackUpdate, Details: `{"removed":true}`,
		})
	})
	if err != nil {
		return err
	}
	return c.noContent()
}

func (s *Server) testSlack(c *call) error {
	ctx := c.r.Context()
	oc, err := s.loadOrg(c, RoleOwner)
	if err != nil {
		return err
	}
	cfg, err := s.store.GetSlackConfig(ctx, oc.inst.ID)
	if errors.Is(err, model.ErrNotFound) || (err == nil && cfg.WebhookURL == "") {
		return errBadRequest("Slack is not configured", nil)
	}
	if err != nil {
		return err
	}
	if s.slack == nil {
		return &apiError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Slack delivery is not available"}
	}
	err = s.slack.SendTest(ctx, oc.inst.ID, oc.inst.AccountLogin, s.dashboardLink("/orgs/"+oc.inst.AccountLogin))
	switch {
	case err == nil:
		return c.noContent()
	case errors.Is(err, slack.ErrNotConfigured):
		return errBadRequest("Slack is not configured", nil)
	}
	// slack errors never contain the webhook URL.
	s.log.Warn("slack test message failed", "org", oc.inst.AccountLogin, "err", err)
	return errBadRequest("Slack rejected the test message", nil)
}

// dashboardLink joins the configured dashboard base URL and path; "" when no
// base URL is configured.
func (s *Server) dashboardLink(path string) string {
	if s.cfg.DashboardURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.DashboardURL, "/") + path
}
