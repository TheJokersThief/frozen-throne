// Package api implements the HTTP server described by api/openapi.yaml:
// GitHub OAuth login with signed-cookie sessions, live GitHub permission
// checks, and the freeze / schedule / bypass / audit endpoints.
//
// Everything that talks to the outside world sits behind an interface so it
// can be faked in tests: OAuthProvider (GitHub login), PermissionChecker
// (what a user may do on GitHub), engine.Evaluator (freeze evaluation) and
// ChangeNotifier (told after every committed state change so the reconcile
// loop can re-evaluate Check Runs).
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "time/tzdata" // IANA zones must resolve even on minimal images

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/slack"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// Prefix is the path prefix every API route is served under.
const Prefix = "/api/v1"

const (
	sessionCookie = "ft_session"
	stateCookie   = "ft_oauth"
	csrfHeader    = "X-Requested-With"
	csrfValue     = "frozen-throne"
)

// FreezeChange describes a committed change that can alter freeze state or a
// PR's check. It is delivered to the ChangeNotifier after the transaction
// commits.
type FreezeChange struct {
	InstallationID int64
	// RepoID is nil when the change is org-wide (org freeze or org window).
	RepoID *int64
	// PRNumber is set for bypass changes (a single PR must be re-checked).
	PRNumber int
	// Action is the audit action that caused the change.
	Action model.AuditAction
	// Actor is the login that made the change (empty for system changes).
	Actor string
	// Reason is the freeze or bypass reason, when there is one.
	Reason string
	// Scope is org or repo for freeze/thaw changes.
	Scope model.FreezeScope
}

// ChangeNotifier receives "freeze state changed" events. Implementations must
// not block for long; the reconcile loop is expected to enqueue and return.
type ChangeNotifier interface {
	FreezeStateChanged(ctx context.Context, ev FreezeChange)
}

// NopNotifier ignores events.
type NopNotifier struct{}

// FreezeStateChanged implements ChangeNotifier.
func (NopNotifier) FreezeStateChanged(context.Context, FreezeChange) {}

// SlackSender delivers Slack notifications. *slack.Notifier implements it.
type SlackSender interface {
	Notify(ev slack.Event)
	SendTest(ctx context.Context, installationID int64, org, dashboardURL string) error
}

// Config holds static settings.
type Config struct {
	// SessionSecret signs cookies and encrypts stored GitHub tokens. At
	// least 32 bytes of randomness.
	SessionSecret []byte
	// SessionTTL is the lifetime of a login (default 8h).
	SessionTTL time.Duration
	// SecureCookies sets the Secure flag; enable whenever served over HTTPS.
	SecureCookies bool
	// PermissionCacheTTL is how long GitHub permission answers are cached
	// (default 1 minute; negative disables caching).
	PermissionCacheTTL time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
	// Logger receives server-side errors (default slog.Default()).
	Logger *slog.Logger
	// DashboardURL is the public base URL of the web UI, used for links in
	// Slack messages (no trailing slash). Empty omits the links.
	DashboardURL string
	// SlackAllowAnyHost lets webhook URLs point anywhere, including plain
	// http (tests and local development only).
	SlackAllowAnyHost bool
}

// Deps are the collaborators of the server.
type Deps struct {
	Store       store.Store
	Engine      engine.Evaluator
	OAuth       OAuthProvider
	Permissions PermissionChecker
	Notifier    ChangeNotifier // optional
	Slack       SlackSender    // optional; nil disables Slack notifications
}

// Server is the HTTP API.
type Server struct {
	cfg      Config
	store    store.Store
	engine   engine.Evaluator
	oauth    OAuthProvider
	perms    PermissionChecker
	notifier ChangeNotifier
	slack    SlackSender
	sess     *sessionCodec
	log      *slog.Logger
	mux      *http.ServeMux
}

// New builds a Server and registers its routes.
func New(cfg Config, d Deps) (*Server, error) {
	if len(cfg.SessionSecret) < 32 {
		return nil, errors.New("api: SessionSecret must be at least 32 bytes")
	}
	if d.Store == nil || d.Engine == nil || d.OAuth == nil || d.Permissions == nil {
		return nil, errors.New("api: Store, Engine, OAuth and Permissions are required")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		store:    d.Store,
		engine:   d.Engine,
		oauth:    d.OAuth,
		perms:    d.Permissions,
		notifier: d.Notifier,
		slack:    d.Slack,
		sess:     newSessionCodec(cfg.SessionSecret),
		log:      cfg.Logger,
		mux:      http.NewServeMux(),
	}
	if s.notifier == nil {
		s.notifier = NopNotifier{}
	}
	switch ttl := cfg.PermissionCacheTTL; {
	case ttl == 0:
		s.perms = NewCachedPermissions(s.perms, time.Minute, cfg.Now)
	case ttl > 0:
		s.perms = NewCachedPermissions(s.perms, ttl, cfg.Now)
	}
	s.routes()
	return s, nil
}

// Handler returns the root handler (API under Prefix plus /healthz).
func (s *Server) Handler() http.Handler { return s.recoverer(s.mux) }

func (s *Server) now() time.Time { return s.cfg.Now().UTC() }

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "panic", fmt.Sprint(v), "path", r.URL.Path)
				writeError(w, &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handle registers h for "METHOD /path" (path relative to Prefix). When auth
// is true a valid session is required.
func (s *Server) handle(pattern string, auth bool, h func(*call) error) {
	method, path, _ := strings.Cut(pattern, " ")
	s.mux.HandleFunc(method+" "+Prefix+path, func(w http.ResponseWriter, r *http.Request) {
		c := &call{w: w, r: r, s: s}
		if err := s.dispatch(c, auth, h); err != nil {
			s.fail(c, err)
		}
	})
}

func (s *Server) dispatch(c *call, auth bool, h func(*call) error) error {
	r := c.r
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if r.Header.Get(csrfHeader) != csrfValue {
			return errForbidden("missing " + csrfHeader + " header")
		}
		if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
			ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
			if ct != "application/json" {
				return errBadRequest("Content-Type must be application/json", nil)
			}
		}
	}
	if auth {
		a, err := s.authenticate(r)
		if err != nil {
			return err
		}
		c.actor = a
	}
	return h(c)
}

func (s *Server) fail(c *call, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, ErrTokenInvalid):
		// The user's GitHub token no longer works: end the session.
		if c.actor != nil {
			_ = s.store.DeleteSession(c.r.Context(), c.actor.SessionID)
		}
		s.clearSessionCookie(c.w)
		ae = errUnauthorized("GitHub session expired; log in again")
	case errors.Is(err, model.ErrNotFound):
		ae = errNotFound()
	case errors.Is(err, model.ErrConflict):
		ae = &apiError{Status: http.StatusConflict, Code: "conflict", Message: "conflict"}
	case errors.Is(err, model.ErrInvalid), errors.Is(err, model.ErrReasonRequired):
		ae = errBadRequest(err.Error(), nil)
	default:
		s.log.Error("request failed", "method", c.r.Method, "path", c.r.URL.Path, "err", err)
		ae = &apiError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
	}
	writeError(c.w, ae)
}

func (s *Server) routes() {
	healthz := func(c *call) error { return c.json(http.StatusOK, map[string]string{"status": "ok"}) }
	s.handle("GET /healthz", false, healthz)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = (&call{w: w, r: r, s: s}).json(http.StatusOK, map[string]string{"status": "ok"})
	})

	s.handle("GET /auth/login", false, s.login)
	s.handle("GET /auth/callback", false, s.loginCallback)
	s.handle("POST /auth/logout", false, s.logout)
	s.handle("GET /auth/me", true, s.getMe)

	s.handle("GET /repos", true, s.listRepos)
	s.handle("GET /repos/{owner}/{repo}", true, s.getRepo)
	s.handle("POST /repos/{owner}/{repo}/freeze", true, s.freezeRepo)
	s.handle("DELETE /repos/{owner}/{repo}/freeze", true, s.thawRepo)
	s.handle("PUT /repos/{owner}/{repo}/public-status", true, s.setPublicStatus)

	s.handle("GET /orgs", true, s.listOrgs)
	s.handle("GET /orgs/{org}", true, s.getOrg)
	s.handle("POST /orgs/{org}/freeze", true, s.freezeOrg)
	s.handle("DELETE /orgs/{org}/freeze", true, s.thawOrg)

	s.handle("GET /orgs/{org}/schedules", true, s.listOrgSchedules)
	s.handle("POST /orgs/{org}/schedules", true, s.createOrgSchedule)
	s.handle("GET /repos/{owner}/{repo}/schedules", true, s.listRepoSchedules)
	s.handle("POST /repos/{owner}/{repo}/schedules", true, s.createRepoSchedule)
	s.handle("GET /schedules/{id}", true, s.getSchedule)
	s.handle("PUT /schedules/{id}", true, s.updateSchedule)
	s.handle("DELETE /schedules/{id}", true, s.deleteSchedule)

	s.handle("GET /repos/{owner}/{repo}/bypasses", true, s.listBypasses)
	s.handle("POST /repos/{owner}/{repo}/pulls/{number}/bypass", true, s.createBypass)
	s.handle("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", true, s.deleteBypass)

	s.handle("GET /audit", true, s.listAudit)

	s.handle("GET /orgs/{org}/slack", true, s.getSlack)
	s.handle("PUT /orgs/{org}/slack", true, s.putSlack)
	s.handle("DELETE /orgs/{org}/slack", true, s.deleteSlack)
	s.handle("POST /orgs/{org}/slack/test", true, s.testSlack)

	s.handle("GET /public/repos/{owner}/{repo}/status", false, s.getPublicStatus)
	s.handle("GET /public/repos/{owner}/{repo}/badge.svg", false, s.getPublicBadge)
}

// Trigger is the subset of the reconciler the API needs. *reconcile.Reconciler
// implements it.
type Trigger interface {
	Repo(repoID int64)
	Installation(installationID int64)
}

type triggerNotifier struct{ t Trigger }

// NewTriggerNotifier adapts a reconciler into a ChangeNotifier: repo-scoped
// changes (including a bypass on one PR) re-evaluate that repo, org-wide
// changes re-evaluate the installation.
func NewTriggerNotifier(t Trigger) ChangeNotifier { return triggerNotifier{t} }

// FreezeStateChanged implements ChangeNotifier.
func (n triggerNotifier) FreezeStateChanged(_ context.Context, ev FreezeChange) {
	if ev.RepoID != nil {
		n.t.Repo(*ev.RepoID)
		return
	}
	n.t.Installation(ev.InstallationID)
}
