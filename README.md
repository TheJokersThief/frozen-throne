# Frozen Throne (Merge Freezes)

Merge-freeze tooling for GitHub. A GitHub App posts a Check Run on every pull
request: it fails while the repo (or its whole org) is frozen and passes
otherwise. Make the check required in branch protection and no PR can merge
while frozen. Freezes can be manual (repo or org-wide) or scheduled (one-off or
recurring windows with a timezone), and individual PRs can be bypassed with a
recorded reason.

## Self-hosting

One container (or one static binary with the dashboard embedded), one SQLite
file. Follow the [setup guide](docs/setup.md): create the GitHub App, set the
[environment variables](docs/configuration.md), install the App, and require
the `frozen-throne` check in branch protection.

```sh
cp docker-compose.example.yml docker-compose.yml && cp .env.example .env
# edit .env, add the App private key, then:
docker compose up -d
```

Images: `ghcr.io/thejokersthief/frozen-throne`. Binaries for linux/darwin
(amd64, arm64) are attached to each GitHub release.

## Layout

| Path | What lives there |
|------|------------------|
| `cmd/frozen-throne` | Main binary. |
| `internal/model` | Domain types shared by all packages (no dependencies). |
| `internal/store` | Store interfaces (`store.go`), embedded SQL migrations (`migrations/`) and the SQLite implementation. |
| `internal/engine` | Pure freeze evaluation: "is repo X frozen at T, considering bypass for PR N". Interface and types only for now. |
| `internal/github` | GitHub App: webhooks, Check Runs (to come). |
| `internal/reconcile` | Keeps Check Runs correct: triggers, window-boundary timers and a periodic pass feed a fair, bounded work queue with rate-limit backoff. |
| `internal/api` | HTTP handlers, OAuth, permissions (to come). |
| `api/openapi.yaml` | The REST API contract. Source of truth for handlers and the web client. |
| `web/` | React + TypeScript (Vite) dashboard. |
| `internal/config` | Environment-variable configuration. |
| `internal/webui` | Embeds the built dashboard (`web/dist`) and serves it with SPA fallback. |
| `docs/` | Setup guide and configuration reference. |

Design rules that the schema and contracts encode: one deployment serves many
GitHub installations; an org-wide freeze or window is stored once per
installation and so also covers repos added later; a repo is frozen if any of
org freeze, repo freeze or active window applies; a bypass exempts one PR and
needs a reason.

## Develop

Requirements: Go (see `go.mod`), Node 22 and [just](https://github.com/casey/just).

```sh
just build        # go build into bin/
just test         # go test ./...
just lint         # go vet, gofmt check, OpenAPI lint, web typecheck
just web-build    # production web build into web/dist
just embed-web    # web-build, then copy into the Go embed dir (go build then yields a full binary)
just release-build  # static binaries for linux/darwin amd64+arm64 into dist/
just image        # docker build
just check        # everything CI runs
```

### API types

`web/src/api/schema.d.ts` is generated from `api/openapi.yaml` and committed.
After editing the spec run:

```sh
just gen-api      # or: cd web && npm run gen:api
just lint-api     # validate the spec (redocly)
```

CI fails if the committed types are out of date.

### Web dashboard

React + TypeScript + Vite in `web/`. Pages: all repos at a glance (search, filter by
org / frozen state, quick freeze/thaw with confirm + reason, org-wide freeze), repo
detail (reasons, freeze windows, per-PR bypass, public status, recent audit), org
page (org freeze + org-wide windows) and the audit log.

Run it **without a backend** against a mocked API (MSW implements `api/openapi.yaml`
in the browser with seeded data and the freeze semantics: any-of org/repo/window,
bypasses cleared on thaw, permission checks per role):

```sh
cd web
npm ci
npm run dev:mock      # http://localhost:5173, signed in as "octocat"
```

Run against the real server instead with `npm run dev` (proxies `/api` to
`localhost:8080`). Other scripts: `npm test` (component tests, same mock handlers),
`npm run typecheck`, `npm run build`. The mock is only bundled when `VITE_MOCK=1`
(`npm run build:mock`); a normal build contains no mock code. The app uses
client-side routes, so the server must serve `index.html` for non-`/api` paths.

### Database

SQLite via the pure-Go `modernc.org/sqlite` driver (no cgo, so the binary stays
static). Migrations in `internal/store/migrations/NNNN_name.sql` are embedded and
applied in order by `store.Open`. Add a new numbered file; never edit an applied one.

## License

See [LICENSE](LICENSE).
