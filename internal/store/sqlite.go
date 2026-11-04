package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	_ "modernc.org/sqlite" // pure-Go driver: keeps the binary static
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// querier is the subset of *sql.DB / *sql.Tx the implementation uses.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLite is the SQLite implementation of Store.
type SQLite struct {
	db *sql.DB // nil when bound to a transaction
	q  querier
}

var _ Store = (*SQLite)(nil)

// Open opens (creating if needed) the database at path and applies pending
// migrations. Use ":memory:" only for single-connection tests; the pool is
// capped at one connection in that case.
func Open(ctx context.Context, path string) (*SQLite, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// _txlock=immediate: every transaction takes the write lock at BEGIN and
	// waits for it (busy_timeout). A deferred transaction that reads and then
	// writes fails at once with SQLITE_BUSY if another connection committed in
	// between, which the API, webhook handlers and reconciler routinely do.
	dsn += sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &SQLite{db: db, q: db}, nil
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, each in its own transaction, in filename order.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("migration %q: filename must start with a number", name)
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			version, name, fmtTime(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *SQLite) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// WithTx implements Store.
func (s *SQLite) WithTx(ctx context.Context, fn func(Store) error) error {
	if s.db == nil { // already in a transaction
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&SQLite{q: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- helpers ----

const timeLayout = "2006-01-02T15:04:05.000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// mapErr converts driver errors into the model sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotFound
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return fmt.Errorf("%w: %v", model.ErrConflict, err)
	case strings.Contains(msg, "CHECK constraint failed"),
		strings.Contains(msg, "FOREIGN KEY constraint failed"),
		strings.Contains(msg, "NOT NULL constraint failed"):
		return fmt.Errorf("%w: %v", model.ErrInvalid, err)
	}
	return err
}

type scanner interface{ Scan(dest ...any) error }

// exec runs a statement and returns rows affected.
func (s *SQLite) exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// where builds a " WHERE a AND b" suffix.
type where struct {
	parts []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.parts = append(w.parts, cond)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.parts) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.parts, " AND ")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
