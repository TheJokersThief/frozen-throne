package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// ---- freezes ----

const freezeCols = `id, scope, installation_id, repo_id, reason, created_by, created_by_id, created_at, ended_at, ended_by, ended_by_id`

func scanFreeze(r scanner) (model.Freeze, error) {
	var f model.Freeze
	var scope string
	var repo sql.NullInt64
	var created string
	var ended sql.NullString
	if err := r.Scan(&f.ID, &scope, &f.InstallationID, &repo, &f.Reason, &f.CreatedBy, &f.CreatedByID, &created, &ended, &f.EndedBy, &f.EndedByID); err != nil {
		return f, mapErr(err)
	}
	f.Scope = model.FreezeScope(scope)
	if repo.Valid {
		v := repo.Int64
		f.RepoID = &v
	}
	f.CreatedAt = parseTime(created)
	f.EndedAt = parseTimePtr(ended)
	return f, nil
}

func (s *SQLite) CreateFreeze(ctx context.Context, f model.Freeze) (model.Freeze, error) {
	switch {
	case f.Scope == model.FreezeScopeOrg && f.RepoID == nil:
	case f.Scope == model.FreezeScopeRepo && f.RepoID != nil:
	default:
		return f, fmt.Errorf("%w: scope %q does not match repo_id", model.ErrInvalid, f.Scope)
	}
	f.CreatedAt = nowOr(f.CreatedAt)
	res, err := s.q.ExecContext(ctx, `INSERT INTO freezes(scope, installation_id, repo_id, reason, created_by, created_by_id, created_at)
		VALUES (?,?,?,?,?,?,?)`, string(f.Scope), f.InstallationID, nullInt(f.RepoID), f.Reason, f.CreatedBy, f.CreatedByID, fmtTime(f.CreatedAt))
	if err != nil {
		return f, mapErr(err)
	}
	f.ID, _ = res.LastInsertId()
	f.CreatedAt = parseTime(fmtTime(f.CreatedAt))
	return f, nil
}

func (s *SQLite) GetFreeze(ctx context.Context, id int64) (model.Freeze, error) {
	return scanFreeze(s.q.QueryRowContext(ctx, `SELECT `+freezeCols+` FROM freezes WHERE id = ?`, id))
}

func (s *SQLite) EndFreeze(ctx context.Context, id int64, by ThawInfo) (model.Freeze, error) {
	n, err := s.exec(ctx, `UPDATE freezes SET ended_at = ?, ended_by = ?, ended_by_id = ? WHERE id = ? AND ended_at IS NULL`,
		fmtTime(nowOr(by.At)), by.By, by.ByID, id)
	if err != nil {
		return model.Freeze{}, err
	}
	if n == 0 {
		if _, err := s.GetFreeze(ctx, id); err != nil {
			return model.Freeze{}, err
		}
		return model.Freeze{}, fmt.Errorf("%w: freeze %d already ended", model.ErrConflict, id)
	}
	return s.GetFreeze(ctx, id)
}

func (s *SQLite) ActiveOrgFreeze(ctx context.Context, installationID int64) (model.Freeze, error) {
	return scanFreeze(s.q.QueryRowContext(ctx, `SELECT `+freezeCols+` FROM freezes
		WHERE scope = 'org' AND installation_id = ? AND ended_at IS NULL`, installationID))
}

func (s *SQLite) ActiveRepoFreeze(ctx context.Context, repoID int64) (model.Freeze, error) {
	return scanFreeze(s.q.QueryRowContext(ctx, `SELECT `+freezeCols+` FROM freezes
		WHERE scope = 'repo' AND repo_id = ? AND ended_at IS NULL`, repoID))
}

func (s *SQLite) ListFreezes(ctx context.Context, f FreezeFilter) ([]model.Freeze, error) {
	var w where
	if f.InstallationID != 0 {
		w.add("installation_id = ?", f.InstallationID)
	}
	if f.RepoID != nil {
		w.add("repo_id = ?", *f.RepoID)
	}
	if f.Scope != "" {
		w.add("scope = ?", string(f.Scope))
	}
	if f.ActiveOnly {
		w.add("ended_at IS NULL")
	}
	q := `SELECT ` + freezeCols + ` FROM freezes` + w.sql() + ` ORDER BY created_at DESC, id DESC`
	args := w.args
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Freeze
	for rows.Next() {
		fr, err := scanFreeze(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// ---- schedules ----

const scheduleCols = `id, installation_id, repo_id, name, reason, kind, timezone, enabled, starts_at, ends_at,
	weekdays, start_time, end_time, valid_from, valid_until, created_by, created_by_id, created_at, updated_at`

func scanSchedule(r scanner) (model.Schedule, error) {
	var s model.Schedule
	var repo sql.NullInt64
	var kind string
	var enabled, weekdays int
	var starts, ends, vfrom, vuntil sql.NullString
	var created, updated string
	if err := r.Scan(&s.ID, &s.InstallationID, &repo, &s.Name, &s.Reason, &kind, &s.Timezone, &enabled, &starts, &ends,
		&weekdays, &s.StartTime, &s.EndTime, &vfrom, &vuntil, &s.CreatedBy, &s.CreatedByID, &created, &updated); err != nil {
		return s, mapErr(err)
	}
	if repo.Valid {
		v := repo.Int64
		s.RepoID = &v
	}
	s.Kind = model.ScheduleKind(kind)
	s.Enabled = enabled != 0
	s.StartsAt, s.EndsAt = parseTimePtr(starts), parseTimePtr(ends)
	s.Weekdays = model.Weekdays(weekdays)
	s.ValidFrom, s.ValidUntil = ptrStr(vfrom), ptrStr(vuntil)
	s.CreatedAt, s.UpdatedAt = parseTime(created), parseTime(updated)
	return s, nil
}

func validateSchedule(s model.Schedule) error {
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" {
		return fmt.Errorf("%w: unknown timezone %q", model.ErrInvalid, s.Timezone)
	}
	switch s.Kind {
	case model.ScheduleOneOff:
		if s.StartsAt == nil || s.EndsAt == nil || !s.StartsAt.Before(*s.EndsAt) {
			return fmt.Errorf("%w: one-off window needs starts_at < ends_at", model.ErrInvalid)
		}
	case model.ScheduleRecurring:
		if s.Weekdays == 0 || s.Weekdays > 127 {
			return fmt.Errorf("%w: recurring window needs at least one weekday", model.ErrInvalid)
		}
		for _, hm := range []string{s.StartTime, s.EndTime} {
			if _, err := time.Parse("15:04", hm); err != nil {
				return fmt.Errorf("%w: time %q must be HH:MM", model.ErrInvalid, hm)
			}
		}
		for _, d := range []*string{s.ValidFrom, s.ValidUntil} {
			if d == nil {
				continue
			}
			if _, err := time.Parse("2006-01-02", *d); err != nil {
				return fmt.Errorf("%w: date %q must be YYYY-MM-DD", model.ErrInvalid, *d)
			}
		}
	default:
		return fmt.Errorf("%w: unknown schedule kind %q", model.ErrInvalid, s.Kind)
	}
	return nil
}

func (s *SQLite) CreateSchedule(ctx context.Context, in model.Schedule) (model.Schedule, error) {
	if err := validateSchedule(in); err != nil {
		return in, err
	}
	now := fmtTime(time.Now())
	res, err := s.q.ExecContext(ctx, `INSERT INTO schedules(installation_id, repo_id, name, reason, kind, timezone, enabled, starts_at, ends_at,
		weekdays, start_time, end_time, valid_from, valid_until, created_by, created_by_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.InstallationID, nullInt(in.RepoID), in.Name, in.Reason, string(in.Kind), in.Timezone, b2i(in.Enabled),
		fmtTimePtr(in.StartsAt), fmtTimePtr(in.EndsAt), int(in.Weekdays), in.StartTime, in.EndTime,
		nullStr(in.ValidFrom), nullStr(in.ValidUntil), in.CreatedBy, in.CreatedByID, now, now)
	if err != nil {
		return in, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetSchedule(ctx, id)
}

func (s *SQLite) GetSchedule(ctx context.Context, id int64) (model.Schedule, error) {
	return scanSchedule(s.q.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM schedules WHERE id = ?`, id))
}

func (s *SQLite) UpdateSchedule(ctx context.Context, in model.Schedule) (model.Schedule, error) {
	if err := validateSchedule(in); err != nil {
		return in, err
	}
	n, err := s.exec(ctx, `UPDATE schedules SET repo_id=?, name=?, reason=?, kind=?, timezone=?, enabled=?, starts_at=?, ends_at=?,
		weekdays=?, start_time=?, end_time=?, valid_from=?, valid_until=?, updated_at=? WHERE id = ?`,
		nullInt(in.RepoID), in.Name, in.Reason, string(in.Kind), in.Timezone, b2i(in.Enabled),
		fmtTimePtr(in.StartsAt), fmtTimePtr(in.EndsAt), int(in.Weekdays), in.StartTime, in.EndTime,
		nullStr(in.ValidFrom), nullStr(in.ValidUntil), fmtTime(time.Now()), in.ID)
	if err != nil {
		return in, err
	}
	if n == 0 {
		return in, model.ErrNotFound
	}
	return s.GetSchedule(ctx, in.ID)
}

func (s *SQLite) DeleteSchedule(ctx context.Context, id int64) error {
	n, err := s.exec(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (s *SQLite) ListSchedules(ctx context.Context, f ScheduleFilter) ([]model.Schedule, error) {
	var w where
	if f.InstallationID != 0 {
		w.add("installation_id = ?", f.InstallationID)
	}
	if f.RepoID != nil {
		w.add("repo_id = ?", *f.RepoID)
	}
	if f.OrgWideOnly {
		w.add("repo_id IS NULL")
	}
	if f.AppliesToRepoID != nil {
		w.add("(repo_id IS NULL OR repo_id = ?)", *f.AppliesToRepoID)
	}
	if f.EnabledOnly {
		w.add("enabled = 1")
	}
	rows, err := s.q.QueryContext(ctx, `SELECT `+scheduleCols+` FROM schedules`+w.sql()+` ORDER BY id`, w.args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ---- bypasses ----

const bypassCols = `id, repo_id, pr_number, reason, created_by, created_by_id, created_at, ended_at, end_reason`

func scanBypass(r scanner) (model.Bypass, error) {
	var b model.Bypass
	var created, endReason string
	var ended sql.NullString
	if err := r.Scan(&b.ID, &b.RepoID, &b.PRNumber, &b.Reason, &b.CreatedBy, &b.CreatedByID, &created, &ended, &endReason); err != nil {
		return b, mapErr(err)
	}
	b.CreatedAt = parseTime(created)
	b.EndedAt = parseTimePtr(ended)
	b.EndReason = model.BypassEndReason(endReason)
	return b, nil
}

func (s *SQLite) CreateBypass(ctx context.Context, b model.Bypass) (model.Bypass, error) {
	if err := b.Validate(); err != nil {
		return b, err
	}
	res, err := s.q.ExecContext(ctx, `INSERT INTO bypasses(repo_id, pr_number, reason, created_by, created_by_id, created_at)
		VALUES (?,?,?,?,?,?)`, b.RepoID, b.PRNumber, b.Reason, b.CreatedBy, b.CreatedByID, fmtTime(nowOr(b.CreatedAt)))
	if err != nil {
		return b, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetBypass(ctx, id)
}

func (s *SQLite) GetBypass(ctx context.Context, id int64) (model.Bypass, error) {
	return scanBypass(s.q.QueryRowContext(ctx, `SELECT `+bypassCols+` FROM bypasses WHERE id = ?`, id))
}

func (s *SQLite) ActiveBypass(ctx context.Context, repoID int64, pr int) (model.Bypass, error) {
	return scanBypass(s.q.QueryRowContext(ctx, `SELECT `+bypassCols+` FROM bypasses
		WHERE repo_id = ? AND pr_number = ? AND ended_at IS NULL`, repoID, pr))
}

func (s *SQLite) ListBypasses(ctx context.Context, f BypassFilter) ([]model.Bypass, error) {
	var w where
	if f.InstallationID != 0 {
		w.add("repo_id IN (SELECT id FROM repos WHERE installation_id = ?)", f.InstallationID)
	}
	if f.RepoID != nil {
		w.add("repo_id = ?", *f.RepoID)
	}
	if f.PRNumber != nil {
		w.add("pr_number = ?", *f.PRNumber)
	}
	if f.ActiveOnly {
		w.add("ended_at IS NULL")
	}
	q := `SELECT ` + bypassCols + ` FROM bypasses` + w.sql() + ` ORDER BY created_at DESC, id DESC`
	args := w.args
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Bypass
	for rows.Next() {
		b, err := scanBypass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func validEndReason(r model.BypassEndReason) error {
	switch r {
	case model.BypassEndedRevoked, model.BypassEndedThawed, model.BypassEndedMerged, model.BypassEndedClosed:
		return nil
	}
	return fmt.Errorf("%w: end reason %q", model.ErrInvalid, r)
}

func (s *SQLite) EndBypass(ctx context.Context, id int64, reason model.BypassEndReason, at time.Time) (model.Bypass, error) {
	if err := validEndReason(reason); err != nil {
		return model.Bypass{}, err
	}
	n, err := s.exec(ctx, `UPDATE bypasses SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`,
		fmtTime(nowOr(at)), string(reason), id)
	if err != nil {
		return model.Bypass{}, err
	}
	if n == 0 {
		if _, err := s.GetBypass(ctx, id); err != nil {
			return model.Bypass{}, err
		}
		return model.Bypass{}, fmt.Errorf("%w: bypass %d already ended", model.ErrConflict, id)
	}
	return s.GetBypass(ctx, id)
}

func (s *SQLite) EndBypassesForPR(ctx context.Context, repoID int64, pr int, reason model.BypassEndReason, at time.Time) (int, error) {
	if err := validEndReason(reason); err != nil {
		return 0, err
	}
	n, err := s.exec(ctx, `UPDATE bypasses SET ended_at = ?, end_reason = ? WHERE repo_id = ? AND pr_number = ? AND ended_at IS NULL`,
		fmtTime(nowOr(at)), string(reason), repoID, pr)
	return int(n), err
}

func (s *SQLite) EndBypassesForRepo(ctx context.Context, repoID int64, reason model.BypassEndReason, at time.Time) (int, error) {
	if err := validEndReason(reason); err != nil {
		return 0, err
	}
	n, err := s.exec(ctx, `UPDATE bypasses SET ended_at = ?, end_reason = ? WHERE repo_id = ? AND ended_at IS NULL`,
		fmtTime(nowOr(at)), string(reason), repoID)
	return int(n), err
}
