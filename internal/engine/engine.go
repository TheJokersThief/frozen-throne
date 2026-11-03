// Package engine decides whether a repo is frozen. It is a pure library: no
// I/O, no clock reads, no GitHub. Callers load the inputs (from the store) and
// pass the instant to evaluate at, which keeps it trivially testable.
//
// Semantics (the contract for the implementation task):
//
//   - A repo is frozen if ANY of the following applies at time T: an active
//     org-wide freeze for its installation, an active freeze on the repo, or
//     an enabled schedule window (org-wide or repo-scoped) containing T.
//     Thawing one source never cancels the others. There is no repo-level
//     override of an org freeze.
//   - Windows auto-thaw: a one-off window is [StartsAt, EndsAt); a recurring
//     window is evaluated in its IANA timezone (DST-aware); see
//     model.Schedule for the exact recurrence rules.
//   - A bypass exempts exactly one pull request (not one commit) and survives
//     new pushes. Bypass only matters while the repo is frozen. It does not
//     change the repo's frozen state, only whether that PR is blocked.
//   - Bypass lifetime: it ends when the repo stops being frozen (callers use
//     Decision.Frozen==false to end bypasses with BypassEndedThawed) or when
//     the PR merges or closes. The engine never mutates anything.
package engine

import (
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// SourceKind identifies why a repo is frozen.
type SourceKind string

const (
	SourceOrgFreeze  SourceKind = "org_freeze"
	SourceRepoFreeze SourceKind = "repo_freeze"
	SourceSchedule   SourceKind = "schedule"
)

// Input is everything needed to evaluate one repo at one instant.
type Input struct {
	// Repo is the repository being evaluated.
	Repo model.Repo
	// OrgFreeze is the installation's active org-wide freeze, if any.
	OrgFreeze *model.Freeze
	// RepoFreeze is the repo's active freeze, if any.
	RepoFreeze *model.Freeze
	// Schedules are all windows that may apply: org-wide ones for the
	// repo's installation plus ones scoped to this repo. Disabled windows
	// are ignored.
	Schedules []model.Schedule
	// PR is the pull request number being evaluated, or 0 for a repo-level
	// question ("is the repo frozen?") with no bypass consideration.
	PR int
	// Bypass is the active bypass for PR, if any. Ignored when PR == 0 or
	// when Bypass.PRNumber != PR.
	Bypass *model.Bypass
	// At is the instant to evaluate at.
	At time.Time
}

// Reason is one thing causing the freeze.
type Reason struct {
	Kind SourceKind
	// FreezeID is set for SourceOrgFreeze / SourceRepoFreeze.
	FreezeID int64
	// ScheduleID is set for SourceSchedule.
	ScheduleID int64
	// Text is the human reason given by whoever froze, or the window name.
	Text string
	// Since is when this reason began applying (freeze creation, or window
	// start).
	Since time.Time
	// Until is when it stops applying on its own (window end); nil for manual
	// freezes, which last until thawed.
	Until *time.Time
}

// Decision is the engine's answer.
type Decision struct {
	// Frozen is true if any Reason applies. It is independent of bypass.
	Frozen bool
	// Reasons lists every applying cause, so the UI/check output can say why
	// and thawing one cause visibly leaves the others.
	Reasons []Reason
	// Bypassed is true when Frozen and the PR has an active bypass.
	Bypassed bool
	// BypassID is the bypass that applies when Bypassed.
	BypassID int64
	// Blocked is what the Check Run should reflect: Frozen && !Bypassed.
	// A repo-level evaluation (PR == 0) has Blocked == Frozen.
	Blocked bool
	// NextChange is the earliest future instant at which the result could
	// change by itself because of a schedule (a window starting or ending),
	// or nil if none. It lets a scheduler wake exactly when needed.
	NextChange *time.Time
}

// Evaluator evaluates freeze state.
type Evaluator interface {
	// Evaluate computes the Decision for in. It must not mutate in. It
	// returns an error only for invalid input (e.g. unknown timezone).
	Evaluate(in Input) (Decision, error)
}

// New returns the Evaluator.
func New() Evaluator { return evaluator{} }
