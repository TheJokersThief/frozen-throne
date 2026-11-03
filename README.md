# Frozen Throne (Merge Freezes)

Merge-freeze tooling for GitHub. A GitHub App posts a Check Run on every pull
request: it fails while the repo (or its whole org) is frozen and passes
otherwise. Make the check required in branch protection and no PR can merge
while frozen. Freezes can be manual (repo or org-wide) or scheduled (one-off or
recurring windows with a timezone), and individual PRs can be bypassed with a
recorded reason.

> **Status:** v2 is a rewrite in progress. The production binary is not wired
> up yet, but the real HTTP API (`internal/api`) runs today through a dev-only
> server with a fake GitHub login, so the web dashboard works end to end locally
> (see [Web dashboard](#web-dashboard)).

## Layout

| Path | What lives there |
|------|------------------|
| `cmd/frozen-throne` | Main binary. |
| `internal/model` | Domain types shared by all packages (no dependencies). |
| `internal/store` | Store interfaces (`store.go`), embedded SQL migrations (`migrations/`) and the SQLite implementation. |
| `internal/engine` | Pure freeze evaluation: "is repo X frozen at T, considering bypass for PR N". Interface and types only for now. |
| `internal/github` | GitHub App: webhooks, Check Runs (to come). |
| `internal/api` | HTTP handlers, sessions, OAuth, permissions. |
| `cmd/dev-server` | Dev-only API server: real handlers, fake GitHub login, seeded data. |
| `api/openapi.yaml` | The REST API contract. Source of truth for handlers and the web client. |
| `web/` | React + TypeScript (Vite) dashboard. |
| `docs/` | Design documents. |

Design rules that the schema and contracts encode: one deployment serves many
GitHub installations; an org-wide freeze or window is stored once per
installation and so also covers repos added later; a repo is frozen if any of
org freeze, repo freeze or active window applies; a bypass exempts one PR and
needs a reason.

## Develop

Requirements: Go (see `go.mod`) and Node 22.

```sh
make build        # go build into bin/
make test         # go test ./...
make lint         # go vet, gofmt check, OpenAPI lint, web typecheck
make web-build    # production web build into web/dist
make check        # everything CI runs
```

### API types

`web/src/api/schema.d.ts` is generated from `api/openapi.yaml` and committed.
After editing the spec run:

```sh
make gen-api      # or: cd web && npm run gen:api
make lint-api     # validate the spec (redocly)
```

CI fails if the committed types are out of date.

### Web dashboard

React + TypeScript + Vite in `web/`. Pages: all repos at a glance (search, filter by
org / frozen state, quick freeze/thaw with confirm + reason, org-wide freeze), repo
detail (reasons, freeze windows, per-PR bypass, public status, recent audit), org
page (org freeze + org-wide windows) and the audit log.

#### Run it against a local backend

Requirements: Go and Node 22. Use two terminals.

```sh
# 1. Terminal one, repo root: the real API with a fake GitHub login and seeded data.
make dev-server          # = go run ./cmd/dev-server, listens on :8080

# 2. Terminal two: the dashboard.
cd web && npm ci && npm run dev
```

3. Open <http://localhost:5173> and click **Sign in with GitHub**. The dev server fakes
   the GitHub round trip and signs you in as `octocat`; you land back on the page you
   started from.

`cmd/dev-server` is **DEV ONLY**: anyone who can reach the port is logged in. It takes
flags (pass them to `go run`; `make dev-server` passes none):

| Flag | Default | Meaning |
|------|---------|---------|
| `-addr` | `:8080` | Listen address. |
| `-db` | `./dev.db` | SQLite file, seeded when empty (git-ignored). Delete it to reseed. |
| `-role` | `admin` | Permissions of the dev user: `admin`, `write`, `read` or `none`. |

Set `FT_DEV_SESSION_SECRET` to keep sessions valid across restarts; otherwise a random
secret is generated on each start and old cookies stop working (you sign in again; a
page that was already open shows "Your session expired, sign in again").

To see permission-denied states run the server with a weaker role, for example
`go run ./cmd/dev-server -role read`. `write` may freeze repos but not orgs; `read` gets
403 on writes; `none` grants nothing. To see the mid-session case, keep
`FT_DEV_SESSION_SECRET` set, restart with a weaker `-role` while the page is open and
try a freeze: the UI shows "You don't have permission to ..." and does not sign you out.

#### How the browser talks to the API

- **Proxy.** `npm run dev` proxies `/api` to `http://localhost:8080`; set `FT_API_TARGET`
  to point elsewhere, e.g. `FT_API_TARGET=http://localhost:8081 npm run dev`
  (config in `web/proxy.ts`). Because the browser only ever talks to the Vite origin,
  everything is same-origin: no CORS.
- **Session cookie.** `ft_session` is `HttpOnly` and `SameSite=Lax`, and `Secure` only
  when the server runs with secure cookies (HTTPS; the dev server does not). The client
  sends it with `credentials: "same-origin"`.
- **CSRF.** Every non-GET request carries `X-Requested-With: frozen-throne`; without it
  the API answers 403. The UI treats that as a bug, not as a permission problem.
- **Errors.** 401 returns you to the sign-in screen (with an "expired" message if you were
  signed in), 403 on a write is shown as a permission message and refreshes your
  permissions, 404 means the repo or org does not exist or is not visible to you, and
  5xx or network failures show an error with Retry.

#### Mock mode (no backend)

```sh
cd web && npm ci && npm run dev:mock    # VITE_MOCK=1, http://localhost:5173
```

MSW implements `api/openapi.yaml` in the browser with seeded data and the freeze
semantics (any-of org/repo/window, bypasses cleared on thaw, per-role permissions).
Mock mode is opt-in via `VITE_MOCK=1`; the default is the real API. Production builds
contain no mock code (no MSW, mock db or service worker): `npm run build` runs
`scripts/check-no-mock.mjs` over `dist/` and fails if any leaked. `npm run build:mock`
makes a mock bundle on purpose.

#### Tests and the contract test

```sh
cd web && npm test && npm run typecheck && npm run build
```

Component tests run the whole app against the same mock handlers.
`web/src/mocks/contract.test.ts` validates every mock handler (requests and responses,
status codes, error shapes) against `api/openapi.yaml`, so the mock cannot drift from
the spec. When the spec changes:

1. Edit `api/openapi.yaml`, then `make lint-api`.
2. Regenerate types: `make gen-api` (commit `web/src/api/schema.d.ts`).
3. Run `npm test`; the contract test reports each route or status code whose mock
   (`web/src/mocks/handlers.ts`, `db.ts`) no longer matches. Fix the mock, and the Go
   handlers in `internal/api`, until it passes. Run `npm run typecheck` too: it covers
   test files.

The app uses client-side routes, so a production server must serve `index.html` for
non-`/api` paths.

### Database

SQLite via the pure-Go `modernc.org/sqlite` driver (no cgo, so the binary stays
static). Migrations in `internal/store/migrations/NNNN_name.sql` are embedded and
applied in order by `store.Open`. Add a new numbered file; never edit an applied one.

## License

See [LICENSE](LICENSE).
