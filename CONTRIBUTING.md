# Contributing

Thanks for helping. Small, focused pull requests that come with tests are the
easiest to review and merge.

## Repo layout

| Path | What lives there |
|------|------------------|
| `cmd/frozen-throne` | The server binary (config from `FT_*` env, `-healthcheck`, `-version`). |
| `cmd/dev-server` | **Dev only.** Real API handlers with a fake GitHub login and seeded data. |
| `internal/model` | Domain types shared by all packages (no dependencies). |
| `internal/store` | Store interfaces (`store.go`), embedded SQL migrations (`migrations/NNNN_name.sql`) and the SQLite implementation. |
| `internal/engine` | Pure freeze evaluation: is repo X frozen at time T, given org/repo freezes, windows and bypasses. |
| `internal/github` | The GitHub App: webhook receiver, App JWT and installation tokens, the `frozen-throne` Check Run, installation sync. |
| `internal/reconcile` | Keeps Check Runs correct: triggers, window-boundary timers and a periodic sweep feed a bounded work queue with rate-limit backoff. |
| `internal/api` | HTTP handlers, OAuth login, permissions, audit log. |
| `internal/slack` | Slack notifications. |
| `internal/config` | Environment-variable configuration. |
| `internal/webui` | Embeds the built dashboard (`internal/webui/dist`, filled by `just embed-web`) and serves it with SPA fallback. |
| `api/openapi.yaml` | **The REST API contract.** Source of truth for the Go handlers, the web client types and the web mock. |
| `web/` | React + TypeScript (Vite) dashboard. `src/mocks/` is an in-browser mock API (MSW) that implements the contract. |
| `test/` | Integration harness: `mockgithub` (fake GitHub), `webhooks` (signed payload fixtures), `app` (test wiring), `integration` (scenarios). See [test/README.md](test/README.md). |
| `docs/` | [Setup guide](docs/setup.md), [configuration reference](docs/configuration.md), `images/` (README screenshots). |

Design rules the schema encodes: one deployment serves many GitHub
installations; an org-wide freeze or window is stored once per installation
(so it also covers repos added later); a repo is frozen if any of org freeze,
repo freeze or active window applies; a bypass exempts one PR and needs a
reason.

## Dev setup

Requirements: Go (version in `go.mod`), Node 22, [just](https://github.com/casey/just).
Docker is optional (integration tests in compose mode, the image).

```sh
git clone https://github.com/TheJokersThief/frozen-throne && cd frozen-throne
just            # list recipes
just check      # lint + Go tests + web tests + builds: what CI runs
```

Run the dashboard against the real API handlers with fake login (two terminals):

```sh
just dev-server                  # terminal 1: 127.0.0.1:8080, seeded SQLite at ./dev.db
cd web && npm ci && npm run dev  # terminal 2: http://localhost:5173, click "Sign in with GitHub"
```

`just dev-server -role read` (any args are forwarded) shows permission-denied
states; flags are `-addr`, `-allow-remote`, `-db`, `-role` (`admin`, `write`,
`read`, `none`). Set `FT_DEV_SESSION_SECRET` (32+ bytes) to keep sessions across
restarts. `npm run dev` proxies `/api` to `:8080`; override with `FT_API_TARGET`.
The dev server grants everyone access: never expose it.

No backend at all: `cd web && npm run dev:mock` serves the contract from an
in-browser mock with seeded data. Production builds contain no mock code
(`npm run build` fails if any leaked).

## Just recipes

| Recipe | What it does |
|--------|--------------|
| `just build` | `go build` into `bin/` |
| `just test` | `go test ./...` |
| `just vet`, `just fmt` | `go vet`; fail if any file is not gofmt'd |
| `just lint` | vet, fmt, OpenAPI lint, web typecheck |
| `just web-test`, `just web-typecheck` | Web unit/component tests, `tsc` |
| `just web-build` | Production web build into `web/dist` |
| `just embed-web` | Web build copied into `internal/webui/dist`, so `go build` yields a full binary |
| `just gen-api`, `just lint-api` | Regenerate `web/src/api/schema.d.ts`; lint `api/openapi.yaml` |
| `just integration`, `just integration-compose` | Integration tests (see below) |
| `just screenshots` | Regenerate `docs/images/*.png` |
| `just release-build`, `just image` | Static binaries into `dist/`; Docker image |
| `just dev-server` | Dev API with fake login |
| `just check` | Everything CI runs (except integration) |

## Tests

Write the failing test first (red), make it pass with the simplest change
(green), then refactor. Bug fixes start with a test that reproduces the bug.

- **Go unit tests**: `just test`. Packages are tested next to the code
  (`internal/engine` is pure and table-driven; the store tests run against real
  SQLite; `internal/api` tests exercise handlers through `httptest`).
- **Web tests**: `just web-test` (Vitest + Testing Library). Component tests run
  the whole app against the mock handlers. `web/src/mocks/contract.test.ts`
  validates every mock handler against `api/openapi.yaml`. Also run
  `just web-typecheck`: it covers test files.
- **Integration tests**: `just integration` drives the real server with signed
  webhooks against a mock GitHub. `INTEGRATION_MODE=auto` (default) uses
  Docker compose if a daemon is available, else runs in-process; force a mode
  with `just integration-compose` or `INTEGRATION_MODE=inprocess just integration`.
  Details: [test/README.md](test/README.md). Plain `go test ./...` never
  compiles them (build tag `integration`).

## Changing the API (contract workflow)

`api/openapi.yaml` is the contract; change it first.

1. Edit `api/openapi.yaml`, then `just lint-api`.
2. `just gen-api` and commit the regenerated `web/src/api/schema.d.ts` (CI
   fails if it is stale).
3. `just web-test`: the contract test names every mock route or status code that
   no longer matches. Fix the mock (`web/src/mocks/handlers.ts`, `db.ts`).
4. Update and test the Go handlers in `internal/api` until they match.
5. `just web-typecheck` for the UI that consumes the new types.

## Database migrations

Add a new numbered file `internal/store/migrations/NNNN_name.sql`; never edit
one that has shipped. They are embedded and applied in order by `store.Open`.

## Adding a webhook fixture or mock GitHub endpoint

- **Webhook fixture** (an event the server should handle): add
  `test/webhooks/fixtures/<name>.json.tmpl`, register it in `specs` in
  `test/webhooks/fixtures.go`, add a typed constructor, and a test in
  `test/webhooks`. Use it from a scenario in `test/integration`.
- **Mock GitHub endpoint** (an API call the server makes to GitHub): add a
  handler in `test/mockgithub`, test it there against the real
  `internal/github` client, and add a control method (under `/_mock/`) if
  tests need to seed or inspect it.
- **Web mock endpoint**: it must be in `api/openapi.yaml` first; implement it in
  `web/src/mocks/handlers.ts` and the contract test will check it.

## Screenshots

The README images are real captures of the dashboard in mock-API mode, so
refresh them when the UI changes:

```sh
cd web && npx playwright install chromium   # once
just screenshots                            # rewrites docs/images/dashboard.png and repo-detail.png
```

The script (`web/scripts/screenshots.mjs`) starts Vite with `VITE_MOCK=1` on a
spare port, captures the pages with headless Chromium and palette-compresses
the PNGs. Seed data lives in `web/src/mocks/db.ts`. Commit the new images.

## Commits and pull requests

- Small PRs, one concern each; branch from `main`, never push to it.
- Commit messages: short imperative subject (`Add recurring window validation`),
  a body that explains why when it is not obvious.
- **No AI or tool attribution** in commits, PR titles or PR descriptions (no
  `Co-Authored-By` lines or "generated with" footers).
- Before opening a PR run `just check` (and `just integration` if you touched
  webhooks, the reconciler or the API server). CI runs the same.
- Describe what changed and how you tested it. Link the issue if there is one.

By contributing you agree your work is released under the [MIT license](LICENSE).
