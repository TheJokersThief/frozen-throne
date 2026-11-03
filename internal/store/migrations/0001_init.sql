-- frozen-throne v2 initial schema.
--
-- Conventions:
--   * Timestamps are TEXT, RFC 3339 in UTC with millisecond precision
--     (e.g. 2026-11-03T10:00:00.000Z), so lexical order == time order.
--   * Booleans are INTEGER 0/1.
--   * GitHub IDs are used as primary keys where they exist.
--   * One deployment serves many installations: every tenant-scoped row is
--     reachable from installations(id) and cascades on delete.
--   * An org-wide freeze/window is a row with no repo_id; it is evaluated by
--     joining on installation_id, so repos added later are covered with no
--     extra writes.

CREATE TABLE installations (
    id            INTEGER PRIMARY KEY,                  -- GitHub installation id
    account_login TEXT    NOT NULL,
    account_id    INTEGER NOT NULL,
    account_type  TEXT    NOT NULL CHECK (account_type IN ('Organization', 'User')),
    suspended_at  TEXT,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE TABLE repos (
    id              INTEGER PRIMARY KEY,                -- GitHub repository id
    installation_id INTEGER NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    owner           TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    default_branch  TEXT    NOT NULL DEFAULT '',
    private         INTEGER NOT NULL DEFAULT 0,
    archived        INTEGER NOT NULL DEFAULT 0,
    public_status   INTEGER NOT NULL DEFAULT 0,         -- opt-in to public status/badge
    removed_at      TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
CREATE INDEX repos_installation_idx ON repos(installation_id);
CREATE UNIQUE INDEX repos_full_name_idx ON repos(owner COLLATE NOCASE, name COLLATE NOCASE);

-- Manual freezes. scope='org': repo_id IS NULL, covers the whole installation.
-- scope='repo': repo_id set. Active while ended_at IS NULL. Never deleted.
CREATE TABLE freezes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    scope           TEXT    NOT NULL CHECK (scope IN ('org', 'repo')),
    installation_id INTEGER NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    repo_id         INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    reason          TEXT    NOT NULL DEFAULT '',
    created_by      TEXT    NOT NULL,
    created_by_id   INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    ended_at        TEXT,
    ended_by        TEXT    NOT NULL DEFAULT '',
    ended_by_id     INTEGER NOT NULL DEFAULT 0,
    CHECK ((scope = 'org' AND repo_id IS NULL) OR (scope = 'repo' AND repo_id IS NOT NULL))
);
-- At most one active freeze per target.
CREATE UNIQUE INDEX freezes_active_org_idx  ON freezes(installation_id) WHERE scope = 'org'  AND ended_at IS NULL;
CREATE UNIQUE INDEX freezes_active_repo_idx ON freezes(repo_id)         WHERE scope = 'repo' AND ended_at IS NULL;
CREATE INDEX freezes_installation_idx ON freezes(installation_id, created_at);

-- Freeze windows. repo_id NULL = org-wide (whole installation).
CREATE TABLE schedules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    installation_id INTEGER NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    repo_id         INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT '',
    kind            TEXT    NOT NULL CHECK (kind IN ('one_off', 'recurring')),
    timezone        TEXT    NOT NULL,                   -- IANA name
    enabled         INTEGER NOT NULL DEFAULT 1,
    -- one-off (UTC instants, half-open [starts_at, ends_at))
    starts_at       TEXT,
    ends_at         TEXT,
    -- recurring (local wall-clock in `timezone`)
    weekdays        INTEGER NOT NULL DEFAULT 0,         -- bitmask, bit0 = Sunday
    start_time      TEXT    NOT NULL DEFAULT '',        -- 'HH:MM'
    end_time        TEXT    NOT NULL DEFAULT '',        -- 'HH:MM' (<= start_time: ends next day)
    valid_from      TEXT,                               -- 'YYYY-MM-DD' local, inclusive
    valid_until     TEXT,
    created_by      TEXT    NOT NULL,
    created_by_id   INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    CHECK (kind <> 'one_off'   OR (starts_at IS NOT NULL AND ends_at IS NOT NULL AND starts_at < ends_at)),
    CHECK (kind <> 'recurring' OR (weekdays BETWEEN 1 AND 127 AND start_time <> '' AND end_time <> ''))
);
CREATE INDEX schedules_installation_idx ON schedules(installation_id, repo_id);

-- Per-PR bypasses. Active while ended_at IS NULL.
CREATE TABLE bypasses (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    pr_number     INTEGER NOT NULL CHECK (pr_number > 0),
    reason        TEXT    NOT NULL CHECK (length(trim(reason)) > 0),
    created_by    TEXT    NOT NULL,
    created_by_id INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL,
    ended_at      TEXT,
    end_reason    TEXT    NOT NULL DEFAULT '' CHECK (end_reason IN ('', 'revoked', 'thawed', 'merged', 'closed'))
);
CREATE UNIQUE INDEX bypasses_active_idx ON bypasses(repo_id, pr_number) WHERE ended_at IS NULL;
CREATE INDEX bypasses_repo_idx ON bypasses(repo_id, created_at);

-- Append-only audit log.
CREATE TABLE audit_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    installation_id INTEGER NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    repo_id         INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    action          TEXT    NOT NULL,
    actor_login     TEXT    NOT NULL DEFAULT '',        -- '' = system
    actor_id        INTEGER NOT NULL DEFAULT 0,
    scope           TEXT    NOT NULL DEFAULT '',
    pr_number       INTEGER,
    reason          TEXT    NOT NULL DEFAULT '',
    details         TEXT    NOT NULL DEFAULT '{}',      -- JSON object
    created_at      TEXT    NOT NULL
);
CREATE INDEX audit_installation_idx ON audit_log(installation_id, id);
CREATE INDEX audit_repo_idx ON audit_log(repo_id, id);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

-- Users logged in through GitHub OAuth (no local accounts).
CREATE TABLE users (
    id         INTEGER PRIMARY KEY,                     -- GitHub user id
    login      TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    avatar_url TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    github_token BLOB    NOT NULL,                      -- encrypted by the caller
    created_at   TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL
);
CREATE INDEX sessions_expires_idx ON sessions(expires_at);

-- One Slack config per installation.
CREATE TABLE slack_configs (
    installation_id INTEGER PRIMARY KEY REFERENCES installations(id) ON DELETE CASCADE,
    webhook_url     TEXT    NOT NULL,
    channel         TEXT    NOT NULL DEFAULT '',
    enabled         INTEGER NOT NULL DEFAULT 1,
    events          TEXT    NOT NULL DEFAULT '[]',      -- JSON array; empty = all
    updated_at      TEXT    NOT NULL
);

-- PRs the App has reported on; drives reconcile and bypass cleanup.
CREATE TABLE pull_requests (
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,
    head_sha   TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL CHECK (state IN ('open', 'closed', 'merged')),
    draft      INTEGER NOT NULL DEFAULT 0,
    check_run  INTEGER NOT NULL DEFAULT 0,
    conclusion TEXT    NOT NULL DEFAULT '',
    updated_at TEXT    NOT NULL,
    PRIMARY KEY (repo_id, number)
);
CREATE INDEX pull_requests_open_idx ON pull_requests(repo_id) WHERE state = 'open';
