# Integration harness

Tests frozen-throne as if its events came from real GitHub, without touching
GitHub.

```
 test (Go, tag "integration")                 docker compose / in-process
 ┌──────────────────────────┐   signed webhooks    ┌───────────┐  REST + OAuth  ┌────────────┐
 │ test/integration         │ ───────────────────► │ ft-test-  │ ─────────────► │ mockgithub │
 │  Stack, Env, Session     │   /webhooks/github   │ app       │                │            │
 │                          │ ◄─────────────────── │ (real     │ ◄───────────── │            │
 │                          │   /api/v1 (cookies)  │ packages) │   installation │            │
 │                          │ ── /_mock control ──────────────────────────────► │            │
 └──────────────────────────┘                      └───────────┘   tokens, JWT   └────────────┘
```

| Package | Role |
| --- | --- |
| `test/mockgithub` | In-memory fake GitHub: App JWT and installation tokens, pulls, check runs, OAuth, user permissions, call recording, fault injection. Control API under `/_mock/` plus the Go `Client`. Command in `cmd/mockgithub`. |
| `test/webhooks` | Embedded realistic webhook payload templates and a `Sender` that signs like GitHub's hookshot. |
| `test/app` | Test-only wiring of the real packages (store, engine, github webhook service, API) configured by `FT_*` env. `cmd/ft-test-app` is its binary. It stands in for `cmd/frozen-throne` while that is a scaffold. |
| `test/integration` | Build tag `integration`. `Stack` launcher, `Env` scenario helpers, tests. |
| `test/Dockerfile`, `test/docker-compose.yml` | Images (targets `app`, `mockgithub`) and the two-service stack. |

## Running

```sh
go test -tags integration -race -count=1 ./test/integration/...
```

Plain `go test ./...` never builds the integration package (every file carries
`//go:build integration`).

### Modes (`INTEGRATION_MODE`)

| Mode | What happens |
| --- | --- |
| `auto` (default) | `compose` if `docker info` and `docker compose version` succeed, otherwise `inprocess`. The choice is printed to stderr and logged by the first test. |
| `compose` | `docker compose -f test/docker-compose.yml -p ft-it-<random> up -d --build --wait` with free host ports in `MOCK_PORT` / `APP_PORT`; `down -v` on exit; `docker compose logs` printed when any test fails or startup fails. The app URL is `http://localhost:APP_PORT` (matches `FT_PUBLIC_URL`), the mock is `http://127.0.0.1:MOCK_PORT`. |
| `inprocess` | The mock and `test/app` run on `httptest` servers in the test process with the same configuration as compose. Fast; no Docker. |
| `external` | Use a running stack: `FT_APP_URL`, `FT_MOCK_URL` (required), `FT_WEBHOOK_SECRET` (default: the compose value), `FT_MOCK_WEB_INTERNAL` (host:port the app uses for the mock's OAuth pages, if not resolvable from the host). |

Env vars summary: `INTEGRATION_MODE`, `FT_APP_URL`, `FT_MOCK_URL`,
`FT_WEBHOOK_SECRET`, `FT_MOCK_WEB_INTERNAL`. Compose itself reads `MOCK_PORT`
and `APP_PORT` (defaults 18081 / 18080 when you run the file by hand:
`docker compose -f test/docker-compose.yml up --build`).

Compose mode needs a Docker daemon. It was written against
`docker compose config` validation only; check it on a machine with Docker
before relying on it.

### OAuth redirects in compose

The app builds its authorize URL from `FT_GITHUB_WEB_URL`, which in compose is
`http://mockgithub:8080` (a container-network name). `Stack.Login` therefore
follows redirects with a hook that rewrites any host listed in
`Stack.HostRewrites` to the host-reachable base URL (compose registers
`mockgithub:8080 -> http://127.0.0.1:MOCK_PORT`). The redirect back to the app
already points at the host-visible `FT_PUBLIC_URL`.

## Test structure

* `TestMain` starts one `Stack` for the package.
* Every test starts with `e := stack.Reset(t)`: the mock is wiped and a fresh
  `Env` is seeded with unique installation, account, repo and user ids and an
  org name never used before. The app's database persists across tests, so
  uniqueness (not cleanup) is the isolation. Tests run serially; do not use
  `t.Parallel()` (the mock is reset globally and the OAuth login uses its single
  default user).
* `e.Install()` sends `installation.created`; send it before repo-level events
  (see the known bug below).
* `e.OpenPR`, `e.Deliver`, `e.WaitCheck`, `e.Freeze(stack.Login(t, e.Maintainer), reason)`,
  `e.ListRepos`, `stack.Mock.Calls` cover most scenarios.

## Adding things

* Webhook fixture: add `test/webhooks/fixtures/<name>.json.tmpl`, register it in
  `specs` in `test/webhooks/fixtures.go`, add a constructor and a receiver test.
* Scenario: add a helper to `test/integration/scenario.go` (see the
  `EXTENSION POINT` comment for reconcile / API-server scenarios) and a
  `*_test.go` with the `integration` tag.
* Mock behaviour: add a handler in `test/mockgithub`, test it there against the
  real `internal/github` client, and add a control method if tests need to seed it.

## Known gaps

* A repo-level event (pull_request, check_run, merge_group) delivered before
  `installation.created` returns 500 in `internal/github` because GitHub's thin
  installation block leaves `account_type` empty. `TestPullRequestBeforeInstallation`
  documents it and is skipped.
* `test/app` has no `ChangeNotifier`, so API freezes do not re-check open PRs.
