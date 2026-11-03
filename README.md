# Frozen Throne (Merge Freezes)

Merge-freeze tooling for GitHub. A GitHub App posts a Check Run on every pull
request: it fails while the repo (or its whole org) is frozen and passes
otherwise. Make the check required in branch protection and no PR can merge
while frozen. Freezes can be manual (repo or org-wide) or scheduled (one-off or
recurring windows with a timezone), and individual PRs can be bypassed with a
recorded reason.

> **Status:** v2 is a rewrite in progress. This branch contains the foundation
> (schema, store, engine contract, OpenAPI spec, web skeleton); the server is
> not runnable yet.

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

### Database

SQLite via the pure-Go `modernc.org/sqlite` driver (no cgo, so the binary stays
static). Migrations in `internal/store/migrations/NNNN_name.sql` are embedded and
applied in order by `store.Open`. Add a new numbered file; never edit an applied one.

## License

See [LICENSE](LICENSE).
