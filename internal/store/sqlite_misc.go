package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// ---- audit ----

const auditCols = `id, installation_id, repo_id, action, actor_login, actor_id, scope, pr_number, reason, details, created_at`

func scanAudit(r scanner) (model.AuditEntry, error) {
	var e model.AuditEntry
	var repo, pr sql.NullInt64
	var action, scope, created string
	if err := r.Scan(&e.ID, &e.InstallationID, &repo, &action, &e.ActorLogin, &e.ActorID, &scope, &pr, &e.Reason, &e.Details, &created); err != nil {
		return e, mapErr(err)
	}
	if repo.Valid {
		v := repo.Int64
		e.RepoID = &v
	}
	if pr.Valid {
		v := int(pr.Int64)
		e.PRNumber = &v
	}
	e.Action, e.Scope, e.CreatedAt = model.AuditAction(action), model.FreezeScope(scope), parseTime(created)
	return e, nil
}

func (s *SQLite) AppendAudit(ctx context.Context, e model.AuditEntry) (model.AuditEntry, error) {
	if e.Action == "" {
		return e, fmt.Errorf("%w: audit action required", model.ErrInvalid)
	}
	if e.Details == "" {
		e.Details = "{}"
	}
	if !json.Valid([]byte(e.Details)) {
		return e, fmt.Errorf("%w: audit details must be JSON", model.ErrInvalid)
	}
	var pr any
	if e.PRNumber != nil {
		pr = *e.PRNumber
	}
	res, err := s.q.ExecContext(ctx, `INSERT INTO audit_log(installation_id, repo_id, action, actor_login, actor_id, scope, pr_number, reason, details, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, e.InstallationID, nullInt(e.RepoID), string(e.Action), e.ActorLogin, e.ActorID,
		string(e.Scope), pr, e.Reason, e.Details, fmtTime(nowOr(e.CreatedAt)))
	if err != nil {
		return e, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return scanAudit(s.q.QueryRowContext(ctx, `SELECT `+auditCols+` FROM audit_log WHERE id = ?`, id))
}

func (s *SQLite) ListAudit(ctx context.Context, f AuditFilter) ([]model.AuditEntry, error) {
	var w where
	if f.InstallationID != 0 {
		w.add("installation_id = ?", f.InstallationID)
	}
	if f.RepoID != nil {
		w.add("repo_id = ?", *f.RepoID)
	}
	if f.Action != "" {
		w.add("action = ?", string(f.Action))
	}
	if f.Actor != "" {
		w.add("actor_login = ? COLLATE NOCASE", f.Actor)
	}
	if f.BeforeID > 0 {
		w.add("id < ?", f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.QueryContext(ctx, `SELECT `+auditCols+` FROM audit_log`+w.sql()+` ORDER BY id DESC LIMIT ?`,
		append(w.args, limit)...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.AuditEntry
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- users & sessions ----

func (s *SQLite) UpsertUser(ctx context.Context, u model.User) error {
	now := fmtTime(time.Now())
	_, err := s.exec(ctx, `INSERT INTO users(id, login, name, avatar_url, created_at, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET login=excluded.login, name=excluded.name, avatar_url=excluded.avatar_url, updated_at=excluded.updated_at`,
		u.ID, u.Login, u.Name, u.AvatarURL, now, now)
	return err
}

func (s *SQLite) GetUser(ctx context.Context, id int64) (model.User, error) {
	var u model.User
	var created, updated string
	err := s.q.QueryRowContext(ctx, `SELECT id, login, name, avatar_url, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Login, &u.Name, &u.AvatarURL, &created, &updated)
	u.CreatedAt, u.UpdatedAt = parseTime(created), parseTime(updated)
	return u, mapErr(err)
}

func (s *SQLite) CreateSession(ctx context.Context, ss model.Session) error {
	now := nowOr(ss.CreatedAt)
	seen := ss.LastSeenAt
	if seen.IsZero() {
		seen = now
	}
	_, err := s.exec(ctx, `INSERT INTO sessions(id, user_id, github_token, created_at, expires_at, last_seen_at) VALUES (?,?,?,?,?,?)`,
		ss.ID, ss.UserID, ss.GitHubToken, fmtTime(now), fmtTime(ss.ExpiresAt), fmtTime(seen))
	return err
}

func (s *SQLite) GetSession(ctx context.Context, id string, now time.Time) (model.Session, error) {
	var ss model.Session
	var created, expires, seen string
	err := s.q.QueryRowContext(ctx, `SELECT id, user_id, github_token, created_at, expires_at, last_seen_at
		FROM sessions WHERE id = ? AND expires_at > ?`, id, fmtTime(now)).
		Scan(&ss.ID, &ss.UserID, &ss.GitHubToken, &created, &expires, &seen)
	ss.CreatedAt, ss.ExpiresAt, ss.LastSeenAt = parseTime(created), parseTime(expires), parseTime(seen)
	return ss, mapErr(err)
}

func (s *SQLite) TouchSession(ctx context.Context, id string, now time.Time) error {
	n, err := s.exec(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, fmtTime(now), id)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (s *SQLite) DeleteSession(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *SQLite) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	n, err := s.exec(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, fmtTime(now))
	return int(n), err
}

// ---- slack ----

func (s *SQLite) GetSlackConfig(ctx context.Context, installationID int64) (model.SlackConfig, error) {
	var c model.SlackConfig
	var enabled int
	var events, updated string
	err := s.q.QueryRowContext(ctx, `SELECT installation_id, webhook_url, channel, enabled, events, updated_at
		FROM slack_configs WHERE installation_id = ?`, installationID).
		Scan(&c.InstallationID, &c.WebhookURL, &c.Channel, &enabled, &events, &updated)
	if err != nil {
		return c, mapErr(err)
	}
	c.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(events), &c.Events)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

func (s *SQLite) UpsertSlackConfig(ctx context.Context, c model.SlackConfig) error {
	events, _ := json.Marshal(append([]string{}, c.Events...))
	_, err := s.exec(ctx, `INSERT INTO slack_configs(installation_id, webhook_url, channel, enabled, events, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(installation_id) DO UPDATE SET webhook_url=excluded.webhook_url, channel=excluded.channel,
		enabled=excluded.enabled, events=excluded.events, updated_at=excluded.updated_at`,
		c.InstallationID, c.WebhookURL, c.Channel, b2i(c.Enabled), string(events), fmtTime(time.Now()))
	return err
}

func (s *SQLite) DeleteSlackConfig(ctx context.Context, installationID int64) error {
	_, err := s.exec(ctx, `DELETE FROM slack_configs WHERE installation_id = ?`, installationID)
	return err
}

// ---- pull requests ----

const prCols = `repo_id, number, head_sha, state, draft, check_run, conclusion, updated_at`

func scanPR(r scanner) (model.PullRequest, error) {
	var p model.PullRequest
	var state, updated string
	var draft int
	if err := r.Scan(&p.RepoID, &p.Number, &p.HeadSHA, &state, &draft, &p.CheckRun, &p.Conclusion, &updated); err != nil {
		return p, mapErr(err)
	}
	p.State, p.Draft, p.UpdatedAt = model.PRState(state), draft != 0, parseTime(updated)
	return p, nil
}

func (s *SQLite) UpsertPullRequest(ctx context.Context, pr model.PullRequest) error {
	if pr.State == "" {
		pr.State = model.PROpen
	}
	_, err := s.exec(ctx, `INSERT INTO pull_requests(`+prCols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(repo_id, number) DO UPDATE SET head_sha=excluded.head_sha, state=excluded.state, draft=excluded.draft,
		check_run = CASE WHEN excluded.check_run <> 0 THEN excluded.check_run ELSE pull_requests.check_run END,
		conclusion = CASE WHEN excluded.check_run <> 0 THEN excluded.conclusion ELSE pull_requests.conclusion END,
		updated_at=excluded.updated_at`,
		pr.RepoID, pr.Number, pr.HeadSHA, string(pr.State), b2i(pr.Draft), pr.CheckRun, pr.Conclusion, fmtTime(nowOr(pr.UpdatedAt)))
	return err
}

func (s *SQLite) GetPullRequest(ctx context.Context, repoID int64, number int) (model.PullRequest, error) {
	return scanPR(s.q.QueryRowContext(ctx, `SELECT `+prCols+` FROM pull_requests WHERE repo_id = ? AND number = ?`, repoID, number))
}

func (s *SQLite) ListOpenPullRequests(ctx context.Context, repoID int64) ([]model.PullRequest, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+prCols+` FROM pull_requests WHERE repo_id = ? AND state = 'open' ORDER BY number`, repoID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.PullRequest
	for rows.Next() {
		p, err := scanPR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLite) SetPullRequestState(ctx context.Context, repoID int64, number int, state model.PRState, at time.Time) error {
	switch state {
	case model.PROpen, model.PRClosed, model.PRMerged:
	default:
		return fmt.Errorf("%w: pr state %q", model.ErrInvalid, state)
	}
	n, err := s.exec(ctx, `UPDATE pull_requests SET state = ?, updated_at = ? WHERE repo_id = ? AND number = ?`,
		string(state), fmtTime(nowOr(at)), repoID, number)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (s *SQLite) SetPullRequestCheck(ctx context.Context, repoID int64, number int, checkRunID int64, conclusion string, at time.Time) error {
	n, err := s.exec(ctx, `UPDATE pull_requests SET check_run = ?, conclusion = ?, updated_at = ? WHERE repo_id = ? AND number = ?`,
		checkRunID, conclusion, fmtTime(nowOr(at)), repoID, number)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}
