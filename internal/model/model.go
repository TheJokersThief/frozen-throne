// Package model holds the domain types shared by the store, engine, GitHub
// and API packages. It has no dependencies on any of them, so every package
// can import it without creating cycles.
//
// Conventions:
//   - All time.Time values are instants; the store persists them in UTC.
//   - IDs that come from GitHub (installation, repo, user) are GitHub's own
//     numeric IDs, never re-numbered locally.
//   - "Org-wide" always means "every repo of one installation", including
//     repos added after the freeze was created.
package model

import (
	"strings"
	"time"
)

// Installation is one GitHub App installation (an org or a user account).
// A single deployment serves many installations.
type Installation struct {
	ID           int64 // GitHub installation ID
	AccountLogin string
	AccountID    int64
	AccountType  string // "Organization" or "User"
	SuspendedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Repo is a repository the App can see through an installation.
type Repo struct {
	ID             int64 // GitHub repository ID
	InstallationID int64
	Owner          string
	Name           string
	DefaultBranch  string
	Private        bool
	Archived       bool
	// PublicStatus is the per-repo opt-in for the public status endpoint and
	// badge. Defaults to false; nothing about a repo is exposed unless true.
	PublicStatus bool
	// RemovedAt is set when the repo is removed from the installation. The
	// row is kept so audit history stays resolvable.
	RemovedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FullName returns "owner/name".
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// FreezeScope says what a Freeze covers.
type FreezeScope string

const (
	// FreezeScopeOrg covers every repo of the installation, including repos
	// added later. It cannot be overridden by a repo.
	FreezeScopeOrg FreezeScope = "org"
	// FreezeScopeRepo covers a single repo.
	FreezeScopeRepo FreezeScope = "repo"
)

// Freeze is a manual freeze (org-wide or one repo). It is active while
// EndedAt is nil; thawing sets EndedAt. Rows are never deleted, so the
// history is kept.
type Freeze struct {
	ID             int64
	Scope          FreezeScope
	InstallationID int64
	RepoID         *int64 // non-nil iff Scope == FreezeScopeRepo
	Reason         string
	CreatedBy      string // GitHub login
	CreatedByID    int64  // GitHub user ID
	CreatedAt      time.Time
	EndedAt        *time.Time
	EndedBy        string // GitHub login, empty if still active
	EndedByID      int64
}

// Active reports whether the freeze has not been thawed.
func (f Freeze) Active() bool { return f.EndedAt == nil }

// ScheduleKind distinguishes one-off from recurring windows.
type ScheduleKind string

const (
	ScheduleOneOff    ScheduleKind = "one_off"
	ScheduleRecurring ScheduleKind = "recurring"
)

// Weekdays is a bitmask of weekdays: bit n is set for time.Weekday(n)
// (bit 0 = Sunday ... bit 6 = Saturday).
type Weekdays uint8

// Has reports whether day is in the set.
func (w Weekdays) Has(day time.Weekday) bool { return w&(1<<uint(day)) != 0 }

// WeekdaysOf builds a set from days.
func WeekdaysOf(days ...time.Weekday) Weekdays {
	var w Weekdays
	for _, d := range days {
		w |= 1 << uint(d)
	}
	return w
}

// Schedule is a freeze window. A repo is frozen while any enabled window that
// applies to it is active.
//
// One-off windows use StartsAt/EndsAt (absolute instants, half-open
// [StartsAt, EndsAt)).
//
// Recurring windows are evaluated in Timezone (an IANA name such as
// "Europe/Dublin") so they follow DST: on each local date whose weekday is in
// Weekdays, the window runs from StartTime to EndTime (local "HH:MM"). If
// EndTime <= StartTime the window ends on the following local day (overnight),
// and is anchored to the weekday it started on. ValidFrom/ValidUntil
// optionally bound the local dates (inclusive, "YYYY-MM-DD") on which a window
// may start.
type Schedule struct {
	ID             int64
	InstallationID int64
	RepoID         *int64 // nil = org-wide (every repo of the installation)
	Name           string
	Reason         string
	Kind           ScheduleKind
	Timezone       string
	Enabled        bool

	// One-off.
	StartsAt *time.Time
	EndsAt   *time.Time

	// Recurring.
	Weekdays   Weekdays
	StartTime  string  // "HH:MM" local
	EndTime    string  // "HH:MM" local
	ValidFrom  *string // "YYYY-MM-DD" local, inclusive
	ValidUntil *string // "YYYY-MM-DD" local, inclusive

	CreatedBy   string
	CreatedByID int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// BypassEndReason says why a bypass stopped applying.
type BypassEndReason string

const (
	BypassEndedRevoked BypassEndReason = "revoked" // removed by a user
	BypassEndedThawed  BypassEndReason = "thawed"  // the repo stopped being frozen
	BypassEndedMerged  BypassEndReason = "merged"  // the PR merged
	BypassEndedClosed  BypassEndReason = "closed"  // the PR closed unmerged
)

// Bypass exempts exactly one pull request (not one commit) from a freeze. It
// survives new pushes and ends when the freeze ends or the PR merges/closes.
// A reason is mandatory. It is active while EndedAt is nil.
type Bypass struct {
	ID          int64
	RepoID      int64
	PRNumber    int
	Reason      string // required, non-blank
	CreatedBy   string
	CreatedByID int64
	CreatedAt   time.Time
	EndedAt     *time.Time
	EndReason   BypassEndReason
}

// Active reports whether the bypass still applies.
func (b Bypass) Active() bool { return b.EndedAt == nil }

// Validate checks invariants the database also enforces.
func (b Bypass) Validate() error {
	if strings.TrimSpace(b.Reason) == "" {
		return ErrReasonRequired
	}
	if b.PRNumber <= 0 {
		return ErrInvalid
	}
	return nil
}

// AuditAction names an auditable event.
type AuditAction string

const (
	AuditFreeze          AuditAction = "freeze"
	AuditThaw            AuditAction = "thaw"
	AuditScheduleCreate  AuditAction = "schedule.create"
	AuditScheduleUpdate  AuditAction = "schedule.update"
	AuditScheduleDelete  AuditAction = "schedule.delete"
	AuditBypassCreate    AuditAction = "bypass.create"
	AuditBypassEnd       AuditAction = "bypass.end"
	AuditSlackUpdate     AuditAction = "slack.update"
	AuditPublicStatusSet AuditAction = "public_status.update"
)

// AuditEntry is one append-only audit log row. Entries are never updated or
// deleted.
type AuditEntry struct {
	ID             int64
	InstallationID int64
	RepoID         *int64 // nil for org-wide events
	Action         AuditAction
	ActorLogin     string // empty for system actions (e.g. window auto-thaw)
	ActorID        int64
	Scope          FreezeScope // "" if not freeze related
	PRNumber       *int
	Reason         string
	// Details is a JSON object with action-specific extra data.
	Details   string
	CreatedAt time.Time
}

// User is a person who logged in through GitHub OAuth. There are no local
// accounts.
type User struct {
	ID        int64 // GitHub user ID
	Login     string
	Name      string
	AvatarURL string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is a logged-in browser session. ID is an opaque lookup key chosen
// by the API layer (store a hash of the cookie secret, not the secret).
type Session struct {
	ID          string
	UserID      int64
	GitHubToken []byte // OAuth access token, already encrypted by the caller
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastSeenAt  time.Time
}

// SlackConfig is the per-installation Slack notification setting.
type SlackConfig struct {
	InstallationID int64
	WebhookURL     string // secret; never returned by the API
	Channel        string // optional display channel
	Enabled        bool
	// Events lists which events notify: any of "freeze", "thaw", "bypass",
	// "schedule". Empty means all.
	Events    []string
	UpdatedAt time.Time
}

// PullRequest is the App's record of a PR it has reported on. It lets the
// reconcile loop find open PRs without listing GitHub, and lets bypasses end
// when a PR merges or closes.
type PullRequest struct {
	RepoID     int64
	Number     int
	HeadSHA    string
	State      PRState
	Draft      bool
	CheckRun   int64  // ID of the Check Run last posted, 0 if none
	Conclusion string // last conclusion posted: "success", "failure" or ""
	UpdatedAt  time.Time
}

// PRState is the lifecycle state of a pull request.
type PRState string

const (
	PROpen   PRState = "open"
	PRClosed PRState = "closed"
	PRMerged PRState = "merged"
)
