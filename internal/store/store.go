// Package store defines the persistence contracts of frozen-throne and a
// SQLite implementation (see sqlite.go).
//
// Other packages depend on the interfaces here, never on SQLite, and can use
// fakes in tests. Error contract for every method: a missing row yields
// model.ErrNotFound, a uniqueness violation yields model.ErrConflict, and
// invalid input yields model.ErrInvalid (wrapped; test with errors.Is).
package store

import (
	"context"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// Store is the full persistence surface. The aggregate exists so a single
// value can be passed around; consumers should accept the narrowest
// sub-interface they need.
type Store interface {
	Installations
	Repos
	Freezes
	Schedules
	Bypasses
	Audit
	Users
	Slack
	PullRequests

	// WithTx runs fn atomically. fn receives a Store bound to the
	// transaction; the transaction commits if fn returns nil and rolls back
	// otherwise. Use it to keep a state change and its audit entry together.
	// Calling WithTx on the Store passed to fn just runs fn in the same
	// transaction.
	WithTx(ctx context.Context, fn func(Store) error) error

	// Close releases the underlying database.
	Close() error
}

// Installations tracks GitHub App installations (fed by `installation`
// events).
type Installations interface {
	// UpsertInstallation inserts or updates by ID.
	UpsertInstallation(ctx context.Context, in model.Installation) error
	GetInstallation(ctx context.Context, id int64) (model.Installation, error)
	ListInstallations(ctx context.Context) ([]model.Installation, error)
	// DeleteInstallation removes an installation and everything under it
	// (repos, freezes, schedules, bypasses, slack config, audit history).
	DeleteInstallation(ctx context.Context, id int64) error
}

// RepoFilter narrows ListRepos. Zero values mean "no constraint".
type RepoFilter struct {
	InstallationID int64
	// Query is a case-insensitive substring match on "owner/name".
	Query string
	// IncludeRemoved includes repos removed from their installation.
	IncludeRemoved bool
	// PublicStatusOnly restricts to repos that opted in to public status.
	PublicStatusOnly bool
	Limit            int // 0 = no limit
	Offset           int
}

// Repos tracks repositories (fed by `installation_repositories` events).
type Repos interface {
	// UpsertRepo inserts or updates by ID. It never changes PublicStatus of
	// an existing repo (use SetPublicStatus) and clears RemovedAt.
	UpsertRepo(ctx context.Context, in model.Repo) error
	GetRepo(ctx context.Context, id int64) (model.Repo, error)
	// GetRepoByName looks up by owner/name (case-insensitive).
	GetRepoByName(ctx context.Context, owner, name string) (model.Repo, error)
	ListRepos(ctx context.Context, f RepoFilter) ([]model.Repo, error)
	// MarkReposRemoved sets RemovedAt on the given repos of an installation.
	MarkReposRemoved(ctx context.Context, installationID int64, repoIDs []int64, at time.Time) error
	SetPublicStatus(ctx context.Context, repoID int64, enabled bool) error
}

// FreezeFilter narrows ListFreezes.
type FreezeFilter struct {
	InstallationID int64
	RepoID         *int64 // only freezes of this repo
	Scope          model.FreezeScope
	ActiveOnly     bool
	Limit          int // 0 = no limit
}

// ThawInfo records who ended a freeze or bypass.
type ThawInfo struct {
	By   string
	ByID int64
	At   time.Time
}

// Freezes stores manual org and repo freezes.
type Freezes interface {
	// CreateFreeze inserts an active freeze, setting ID and CreatedAt if
	// zero. It returns model.ErrConflict if the same target (installation for
	// org scope, repo for repo scope) already has an active freeze.
	CreateFreeze(ctx context.Context, f model.Freeze) (model.Freeze, error)
	GetFreeze(ctx context.Context, id int64) (model.Freeze, error)
	// EndFreeze thaws a freeze. Ending an already-ended freeze returns
	// model.ErrConflict.
	EndFreeze(ctx context.Context, id int64, by ThawInfo) (model.Freeze, error)
	// ActiveOrgFreeze returns the installation's active org-wide freeze, or
	// model.ErrNotFound.
	ActiveOrgFreeze(ctx context.Context, installationID int64) (model.Freeze, error)
	// ActiveRepoFreeze returns the repo's active freeze, or model.ErrNotFound.
	ActiveRepoFreeze(ctx context.Context, repoID int64) (model.Freeze, error)
	// ListFreezes returns matches newest first.
	ListFreezes(ctx context.Context, f FreezeFilter) ([]model.Freeze, error)
}

// ScheduleFilter narrows ListSchedules.
type ScheduleFilter struct {
	InstallationID int64
	// RepoID restricts to windows scoped to exactly this repo.
	RepoID *int64
	// OrgWideOnly restricts to windows with no repo (installation-wide).
	OrgWideOnly bool
	// AppliesToRepoID returns windows that can apply to this repo: its own
	// plus the installation-wide ones. InstallationID must also be set.
	AppliesToRepoID *int64
	// EnabledOnly drops disabled windows.
	EnabledOnly bool
}

// Schedules stores freeze windows (one-off and recurring).
type Schedules interface {
	// CreateSchedule validates and inserts; sets ID/CreatedAt/UpdatedAt.
	// Returns model.ErrInvalid for a bad timezone, time format, empty
	// weekday set (recurring) or StartsAt >= EndsAt (one-off).
	CreateSchedule(ctx context.Context, s model.Schedule) (model.Schedule, error)
	GetSchedule(ctx context.Context, id int64) (model.Schedule, error)
	// UpdateSchedule replaces all mutable fields of the schedule with s.ID.
	UpdateSchedule(ctx context.Context, s model.Schedule) (model.Schedule, error)
	DeleteSchedule(ctx context.Context, id int64) error
	ListSchedules(ctx context.Context, f ScheduleFilter) ([]model.Schedule, error)
}

// BypassFilter narrows ListBypasses.
type BypassFilter struct {
	InstallationID int64
	RepoID         *int64
	PRNumber       *int
	ActiveOnly     bool
	Limit          int
}

// Bypasses stores per-PR bypasses.
type Bypasses interface {
	// CreateBypass validates (reason required) and inserts. Returns
	// model.ErrConflict if the PR already has an active bypass.
	CreateBypass(ctx context.Context, b model.Bypass) (model.Bypass, error)
	GetBypass(ctx context.Context, id int64) (model.Bypass, error)
	// ActiveBypass returns the PR's active bypass or model.ErrNotFound.
	ActiveBypass(ctx context.Context, repoID int64, pr int) (model.Bypass, error)
	ListBypasses(ctx context.Context, f BypassFilter) ([]model.Bypass, error)
	// EndBypass ends one bypass; already-ended returns model.ErrConflict.
	EndBypass(ctx context.Context, id int64, reason model.BypassEndReason, at time.Time) (model.Bypass, error)
	// EndBypassesForPR ends the PR's active bypass if any (PR merged or
	// closed). Returns how many were ended.
	EndBypassesForPR(ctx context.Context, repoID int64, pr int, reason model.BypassEndReason, at time.Time) (int, error)
	// EndBypassesForRepo ends every active bypass in the repo (the repo
	// stopped being frozen). Returns how many were ended.
	EndBypassesForRepo(ctx context.Context, repoID int64, reason model.BypassEndReason, at time.Time) (int, error)
}

// AuditFilter narrows ListAudit. Results are newest first and paginated by
// the ID cursor.
type AuditFilter struct {
	InstallationID int64
	RepoID         *int64
	Action         model.AuditAction
	Actor          string // login
	// BeforeID returns only entries with ID < BeforeID (0 = from newest).
	BeforeID int64
	Limit    int // 0 = default 50
}

// Audit is the append-only audit log.
type Audit interface {
	// AppendAudit inserts an entry, setting ID and CreatedAt if zero.
	AppendAudit(ctx context.Context, e model.AuditEntry) (model.AuditEntry, error)
	ListAudit(ctx context.Context, f AuditFilter) ([]model.AuditEntry, error)
}

// Users stores OAuth users and their sessions.
type Users interface {
	UpsertUser(ctx context.Context, u model.User) error
	GetUser(ctx context.Context, id int64) (model.User, error)
	CreateSession(ctx context.Context, s model.Session) error
	// GetSession returns model.ErrNotFound if the session does not exist or
	// has expired at `now`.
	GetSession(ctx context.Context, id string, now time.Time) (model.Session, error)
	// TouchSession updates LastSeenAt.
	TouchSession(ctx context.Context, id string, now time.Time) error
	DeleteSession(ctx context.Context, id string) error
	// DeleteExpiredSessions removes sessions expired at now; returns count.
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error)
}

// Slack stores per-installation Slack settings.
type Slack interface {
	// GetSlackConfig returns model.ErrNotFound when unset.
	GetSlackConfig(ctx context.Context, installationID int64) (model.SlackConfig, error)
	UpsertSlackConfig(ctx context.Context, c model.SlackConfig) error
	DeleteSlackConfig(ctx context.Context, installationID int64) error
}

// PullRequests tracks PRs the App has evaluated.
type PullRequests interface {
	// UpsertPullRequest inserts or updates by (RepoID, Number).
	UpsertPullRequest(ctx context.Context, pr model.PullRequest) error
	GetPullRequest(ctx context.Context, repoID int64, number int) (model.PullRequest, error)
	ListOpenPullRequests(ctx context.Context, repoID int64) ([]model.PullRequest, error)
	// SetPullRequestState records merged/closed.
	SetPullRequestState(ctx context.Context, repoID int64, number int, state model.PRState, at time.Time) error
	// SetPullRequestCheck records the Check Run last posted for the PR.
	SetPullRequestCheck(ctx context.Context, repoID int64, number int, checkRunID int64, conclusion string, at time.Time) error
}
