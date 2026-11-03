package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// ---- installations ----

const installationCols = `id, account_login, account_id, account_type, suspended_at, created_at, updated_at`

func scanInstallation(r scanner) (model.Installation, error) {
	var i model.Installation
	var susp sql.NullString
	var created, updated string
	if err := r.Scan(&i.ID, &i.AccountLogin, &i.AccountID, &i.AccountType, &susp, &created, &updated); err != nil {
		return i, mapErr(err)
	}
	i.SuspendedAt = parseTimePtr(susp)
	i.CreatedAt, i.UpdatedAt = parseTime(created), parseTime(updated)
	return i, nil
}

func (s *SQLite) UpsertInstallation(ctx context.Context, in model.Installation) error {
	now := fmtTime(time.Now())
	_, err := s.exec(ctx, `INSERT INTO installations(`+installationCols+`) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET account_login=excluded.account_login, account_id=excluded.account_id,
		account_type=excluded.account_type, suspended_at=excluded.suspended_at, updated_at=excluded.updated_at`,
		in.ID, in.AccountLogin, in.AccountID, in.AccountType, fmtTimePtr(in.SuspendedAt), now, now)
	return err
}

func (s *SQLite) GetInstallation(ctx context.Context, id int64) (model.Installation, error) {
	return scanInstallation(s.q.QueryRowContext(ctx, `SELECT `+installationCols+` FROM installations WHERE id = ?`, id))
}

func (s *SQLite) ListInstallations(ctx context.Context) ([]model.Installation, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+installationCols+` FROM installations ORDER BY account_login`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Installation
	for rows.Next() {
		i, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteInstallation(ctx context.Context, id int64) error {
	n, err := s.exec(ctx, `DELETE FROM installations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}

// ---- repos ----

const repoCols = `id, installation_id, owner, name, default_branch, private, archived, public_status, removed_at, created_at, updated_at`

func scanRepo(r scanner) (model.Repo, error) {
	var p model.Repo
	var priv, arch, pub int
	var removed sql.NullString
	var created, updated string
	if err := r.Scan(&p.ID, &p.InstallationID, &p.Owner, &p.Name, &p.DefaultBranch, &priv, &arch, &pub, &removed, &created, &updated); err != nil {
		return p, mapErr(err)
	}
	p.Private, p.Archived, p.PublicStatus = priv != 0, arch != 0, pub != 0
	p.RemovedAt = parseTimePtr(removed)
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return p, nil
}

func (s *SQLite) UpsertRepo(ctx context.Context, in model.Repo) error {
	now := fmtTime(time.Now())
	_, err := s.exec(ctx, `INSERT INTO repos(id, installation_id, owner, name, default_branch, private, archived, public_status, removed_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,0,NULL,?,?)
		ON CONFLICT(id) DO UPDATE SET installation_id=excluded.installation_id, owner=excluded.owner, name=excluded.name,
		default_branch=excluded.default_branch, private=excluded.private, archived=excluded.archived,
		removed_at=NULL, updated_at=excluded.updated_at`,
		in.ID, in.InstallationID, in.Owner, in.Name, in.DefaultBranch, b2i(in.Private), b2i(in.Archived), now, now)
	return err
}

func (s *SQLite) GetRepo(ctx context.Context, id int64) (model.Repo, error) {
	return scanRepo(s.q.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos WHERE id = ?`, id))
}

func (s *SQLite) GetRepoByName(ctx context.Context, owner, name string) (model.Repo, error) {
	return scanRepo(s.q.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos WHERE owner = ? COLLATE NOCASE AND name = ? COLLATE NOCASE`, owner, name))
}

func (s *SQLite) ListRepos(ctx context.Context, f RepoFilter) ([]model.Repo, error) {
	var w where
	if f.InstallationID != 0 {
		w.add("installation_id = ?", f.InstallationID)
	}
	if f.Query != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(f.Query))
		w.add(`lower(owner || '/' || name) LIKE ? ESCAPE '\'`, "%"+esc+"%")
	}
	if !f.IncludeRemoved {
		w.add("removed_at IS NULL")
	}
	if f.PublicStatusOnly {
		w.add("public_status = 1")
	}
	q := `SELECT ` + repoCols + ` FROM repos` + w.sql() + ` ORDER BY owner COLLATE NOCASE, name COLLATE NOCASE`
	args := w.args
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLite) MarkReposRemoved(ctx context.Context, installationID int64, repoIDs []int64, at time.Time) error {
	for _, id := range repoIDs {
		if _, err := s.exec(ctx, `UPDATE repos SET removed_at = ?, updated_at = ? WHERE id = ? AND installation_id = ?`,
			fmtTime(at), fmtTime(at), id, installationID); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) SetPublicStatus(ctx context.Context, repoID int64, enabled bool) error {
	n, err := s.exec(ctx, `UPDATE repos SET public_status = ?, updated_at = ? WHERE id = ?`,
		b2i(enabled), fmtTime(time.Now()), repoID)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}
