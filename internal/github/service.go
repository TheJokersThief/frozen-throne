package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// Store is the slice of the store the GitHub integration uses.
type Store interface {
	store.Installations
	store.Repos
	store.Freezes
	store.Schedules
	store.Bypasses
	store.PullRequests
	store.Audit
}

// ServiceConfig wires a Service. Store, Engine and API are required.
type ServiceConfig struct {
	Store  Store
	Engine engine.Evaluator
	API    API
	// WebhookSecret is the App's webhook secret used to verify signatures.
	WebhookSecret []byte
	Now           func() time.Time
	Logger        *slog.Logger
}

// Service reacts to GitHub webhooks and keeps Check Runs in line with the
// engine's decisions.
type Service struct {
	st     Store
	eng    engine.Evaluator
	api    API
	secret []byte
	now    func() time.Time
	log    *slog.Logger
}

// NewService validates cfg and returns a Service.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Store == nil || cfg.Engine == nil || cfg.API == nil {
		return nil, errors.New("github: Store, Engine and API are required")
	}
	if len(cfg.WebhookSecret) == 0 {
		return nil, errors.New("github: WebhookSecret is required")
	}
	s := &Service{st: cfg.Store, eng: cfg.Engine, api: cfg.API, secret: cfg.WebhookSecret, now: cfg.Now, log: cfg.Logger}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s, nil
}

// evaluate loads the engine inputs for repo (and pr, if > 0) and decides.
func (s *Service) evaluate(ctx context.Context, repo model.Repo, pr int) (engine.Decision, *model.Bypass, error) {
	in := engine.Input{Repo: repo, PR: pr, At: s.now()}
	if f, err := s.st.ActiveOrgFreeze(ctx, repo.InstallationID); err == nil {
		in.OrgFreeze = &f
	} else if !errors.Is(err, model.ErrNotFound) {
		return engine.Decision{}, nil, err
	}
	if f, err := s.st.ActiveRepoFreeze(ctx, repo.ID); err == nil {
		in.RepoFreeze = &f
	} else if !errors.Is(err, model.ErrNotFound) {
		return engine.Decision{}, nil, err
	}
	rid := repo.ID
	scheds, err := s.st.ListSchedules(ctx, store.ScheduleFilter{InstallationID: repo.InstallationID, AppliesToRepoID: &rid, EnabledOnly: true})
	if err != nil {
		return engine.Decision{}, nil, err
	}
	in.Schedules = scheds
	if pr > 0 {
		if b, err := s.st.ActiveBypass(ctx, repo.ID, pr); err == nil {
			in.Bypass = &b
		} else if !errors.Is(err, model.ErrNotFound) {
			return engine.Decision{}, nil, err
		}
	}
	d, err := s.eng.Evaluate(in)
	return d, in.Bypass, err
}

// checkFor renders a Decision as a Check Run result.
func checkFor(sha string, d engine.Decision, b *model.Bypass) CheckRun {
	cr := CheckRun{HeadSHA: sha}
	switch {
	case !d.Frozen:
		cr.Conclusion, cr.Title, cr.Summary = "success", "Not frozen", "No freeze applies to this repository."
	case d.Bypassed:
		cr.Conclusion, cr.Title = "success", "Frozen, bypassed"
		cr.Summary = "This repository is frozen, but this pull request has a bypass."
		if b != nil {
			cr.Summary += "\n\n**Bypass reason:** " + b.Reason
			if b.CreatedBy != "" {
				cr.Summary += "\n\n**Granted by:** @" + b.CreatedBy
			}
		}
		cr.Summary += "\n\n" + reasonList(d)
	default:
		cr.Conclusion, cr.Title = "failure", "Frozen"
		cr.Summary = "Merging is blocked while this repository is frozen.\n\n" + reasonList(d)
	}
	return cr
}

func reasonList(d engine.Decision) string {
	var sb strings.Builder
	sb.WriteString("**Why it is frozen:**\n")
	for _, r := range d.Reasons {
		label := map[engine.SourceKind]string{
			engine.SourceOrgFreeze:  "Organization freeze",
			engine.SourceRepoFreeze: "Repository freeze",
			engine.SourceSchedule:   "Scheduled window",
		}[r.Kind]
		if label == "" {
			label = string(r.Kind)
		}
		sb.WriteString("- " + label)
		if r.Text != "" {
			sb.WriteString(": " + r.Text)
		}
		if r.Until != nil {
			sb.WriteString(" (until " + r.Until.UTC().Format(time.RFC3339) + ")")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// prRef is a pull request to evaluate.
type prRef struct {
	number int
	sha    string
	draft  bool
}

// syncPR evaluates one PR and makes its Check Run match. With force false, a
// PR whose head SHA and conclusion are unchanged is left untouched (saves API
// calls on reconcile). It reports whether a Check Run was written.
func (s *Service) syncPR(ctx context.Context, repo model.Repo, p prRef, force bool) (bool, error) {
	prev, err := s.st.GetPullRequest(ctx, repo.ID, p.number)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return false, err
	}
	known := err == nil

	if err := s.st.UpsertPullRequest(ctx, model.PullRequest{
		RepoID: repo.ID, Number: p.number, HeadSHA: p.sha, State: model.PROpen, Draft: p.draft, UpdatedAt: s.now(),
	}); err != nil {
		return false, err
	}

	d, byp, err := s.evaluate(ctx, repo, p.number)
	if err != nil {
		return false, err
	}
	if !d.Frozen && byp != nil {
		// The freeze ended: bypasses end with it.
		s.endBypass(ctx, repo, byp, model.BypassEndedThawed)
	}
	cr := checkFor(p.sha, d, byp)

	sameSHA := known && prev.HeadSHA == p.sha
	if !force && sameSHA && prev.CheckRun != 0 && prev.Conclusion == cr.Conclusion {
		return false, nil
	}

	id, err := s.postCheck(ctx, repo, cr, prevCheckID(prev, known, sameSHA))
	if err != nil {
		return false, err
	}
	if err := s.st.SetPullRequestCheck(ctx, repo.ID, p.number, id, cr.Conclusion, s.now()); err != nil {
		return false, err
	}
	return true, nil
}

func prevCheckID(prev model.PullRequest, known, sameSHA bool) int64 {
	if known && sameSHA {
		return prev.CheckRun
	}
	return 0
}

// postCheck updates the Check Run with id (if non-zero and still present) or
// creates a new one, returning the ID in use.
func (s *Service) postCheck(ctx context.Context, repo model.Repo, cr CheckRun, id int64) (int64, error) {
	if id != 0 {
		err := s.api.UpdateCheckRun(ctx, repo.InstallationID, repo.Owner, repo.Name, id, cr)
		if err == nil {
			return id, nil
		}
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != 404 {
			return 0, err
		}
		// Check Run vanished; fall through to create a fresh one.
	}
	return s.api.CreateCheckRun(ctx, repo.InstallationID, repo.Owner, repo.Name, cr)
}

// endBypass ends a bypass and records a system audit entry. Failures are
// logged, not returned: a stale bypass must not block the check from posting.
func (s *Service) endBypass(ctx context.Context, repo model.Repo, b *model.Bypass, reason model.BypassEndReason) {
	if _, err := s.st.EndBypass(ctx, b.ID, reason, s.now()); err != nil {
		if !errors.Is(err, model.ErrConflict) {
			s.log.Error("end bypass", "repo", repo.FullName(), "pr", b.PRNumber, "err", err)
		}
		return
	}
	s.auditBypassEnd(ctx, repo, b.PRNumber, reason)
}

func (s *Service) auditBypassEnd(ctx context.Context, repo model.Repo, pr int, reason model.BypassEndReason) {
	details, _ := json.Marshal(map[string]string{"end_reason": string(reason)})
	rid := repo.ID
	if _, err := s.st.AppendAudit(ctx, model.AuditEntry{
		InstallationID: repo.InstallationID, RepoID: &rid, Action: model.AuditBypassEnd,
		PRNumber: &pr, Details: string(details), CreatedAt: s.now(),
	}); err != nil {
		s.log.Error("audit bypass end", "repo", repo.FullName(), "pr", pr, "err", err)
	}
}

// prClosed records a merged/closed PR and ends its bypass.
func (s *Service) prClosed(ctx context.Context, repo model.Repo, number int, merged bool) error {
	state, reason := model.PRClosed, model.BypassEndedClosed
	if merged {
		state, reason = model.PRMerged, model.BypassEndedMerged
	}
	now := s.now()
	// The PR row may not exist if the App never saw the PR open.
	if _, err := s.st.GetPullRequest(ctx, repo.ID, number); errors.Is(err, model.ErrNotFound) {
		if err := s.st.UpsertPullRequest(ctx, model.PullRequest{RepoID: repo.ID, Number: number, State: state, UpdatedAt: now}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := s.st.SetPullRequestState(ctx, repo.ID, number, state, now); err != nil {
		return err
	}
	n, err := s.st.EndBypassesForPR(ctx, repo.ID, number, reason, now)
	if err != nil {
		return err
	}
	if n > 0 {
		s.auditBypassEnd(ctx, repo, number, reason)
	}
	return nil
}

// ReevaluateResult summarises a ReevaluateRepo run.
type ReevaluateResult struct {
	Open    int // open PRs seen on GitHub
	Updated int // Check Runs written
	Closed  int // stored-open PRs found no longer open and closed out
}

// ReevaluateRepo lists the repo's open PRs on GitHub and brings every Check
// Run in line with the current freeze state. PRs the store still thinks are
// open but GitHub no longer lists are marked closed and their bypass ended.
// Unchanged PRs (same head, same conclusion) are not touched, so it is cheap
// to call periodically. Per-PR failures do not stop the sweep; they are
// joined into the returned error.
func (s *Service) ReevaluateRepo(ctx context.Context, repoID int64) (ReevaluateResult, error) {
	var res ReevaluateResult
	repo, err := s.st.GetRepo(ctx, repoID)
	if err != nil {
		return res, err
	}
	if repo.RemovedAt != nil || repo.Archived {
		return res, nil
	}
	prs, err := s.api.ListOpenPullRequests(ctx, repo.InstallationID, repo.Owner, repo.Name)
	if err != nil {
		return res, fmt.Errorf("list open PRs of %s: %w", repo.FullName(), err)
	}
	res.Open = len(prs)
	var errs []error
	live := make(map[int]bool, len(prs))
	for _, p := range prs {
		live[p.Number] = true
		wrote, err := s.syncPR(ctx, repo, prRef{number: p.Number, sha: p.HeadSHA, draft: p.Draft}, false)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s#%d: %w", repo.FullName(), p.Number, err))
			continue
		}
		if wrote {
			res.Updated++
		}
	}
	stored, err := s.st.ListOpenPullRequests(ctx, repo.ID)
	if err != nil {
		errs = append(errs, err)
		return res, errors.Join(errs...)
	}
	for _, p := range stored {
		if live[p.Number] {
			continue
		}
		// Merged vs closed is unknowable here; "closed" ends the bypass either way.
		if err := s.prClosed(ctx, repo, p.Number, false); err != nil {
			errs = append(errs, fmt.Errorf("%s#%d: %w", repo.FullName(), p.Number, err))
			continue
		}
		res.Closed++
	}
	return res, errors.Join(errs...)
}
