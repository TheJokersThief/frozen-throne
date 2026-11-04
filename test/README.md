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
| `test/mockgithub` | In-memory fake GitHub: App JWT and installation tokens, pulls, check runs, OAuth, user permissions, call recording, fault injection, plus a fake Slack incoming webhook (`/_mock/slack/hooks/{hook}`, read back with `Client.SlackPosts`). Control API under `/_mock/` plus the Go `Client`. Command in `cmd/mockgithub`. |
| `test/webhooks` | Embedded realistic webhook payload templates and a `Sender` that signs like GitHub's hookshot. |
| *(the app)* | The real `cmd/frozen-throne` binary. Compose builds it into the `app` image; `inprocess` mode compiles it with `go build` and runs it as a child process. Configured with the production `FT_*` variables, plus `FT_GITHUB_API_URL` / `FT_GITHUB_WEB_URL` (point at the mock), `FT_SLACK_ALLOW_ANY_HOST=true` (lets the Slack webhook be the fake) and `FT_RECONCILE_INTERVAL=2s` (so missed webhooks heal quickly). |
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
| `inprocess` | The mock runs on an `httptest` server in the test process and the real app binary runs as a child process with the same configuration as compose. No Docker; its output is printed when a test fails. |
| `external` | Use a running stack: `FT_APP_URL`, `FT_MOCK_URL` (required), `FT_WEBHOOK_SECRET` (default: the compose value), `FT_MOCK_WEB_INTERNAL` (host:port the app uses for the mock's OAuth pages, if not resolvable from the host). |

Env vars summary: `INTEGRATION_MODE`, `FT_APP_URL`, `FT_MOCK_URL`,
`FT_WEBHOOK_SECRET`, `FT_MOCK_WEB_INTERNAL`. Compose itself reads `MOCK_PORT`
and `APP_PORT` (defaults 18081 / 18080 when you run the file by hand:
`docker compose -f test/docker-compose.yml up --build`).

On Linux the bind-mounted `test/testdata/app-key.pem` must be world-readable (the containers run as uid 65532), e.g. `chmod 644 test/testdata/app-key.pem`.

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
* Scenario: add a helper to `test/integration/scenario*.go` and a
  `*_test.go` with the `integration` tag.
* Mock behaviour: add a handler in `test/mockgithub`, test it there against the
  real `internal/github` client, and add a control method if tests need to seed it.

## End-to-end scenarios

`scenario_*.go` hold the helpers and `*_scenarios_test.go` the scenarios. They
drive the real app through its public API (OAuth login with a seeded mock user)
and signed webhooks, and assert on the check runs the mock recorded:

| File | Scenarios |
| --- | --- |
| `freeze_scenarios_test.go` | repo freeze/thaw flips every open PR; org freeze covers existing and later-added repos; 403 for users without rights changes nothing |
| `schedule_scenarios_test.go` | one-off window opens and closes by itself (real clock, ~11 s); recurring window evaluated in its timezone |
| `lifecycle_scenarios_test.go` | per-PR bypass (survives a push, ends on close / merge / thaw / revoke); a missed webhook heals through the reconciler |
| `observability_scenarios_test.go` | audit log entries, public status and badge only for opted-in repos, Slack messages for freeze and thaw |

No test sleeps for a fixed time. They poll (`Env.Eventually`, `Env.WaitVerdict`,
20 s timeout). A timeout or a failed API expectation prints `Env.Dump()`: every
check run, the GitHub calls the app made for this test's org, and the fake
Slack posts. Negative claims ("nothing changed") are checked against state the
app commits synchronously (API reads, audit log) rather than by waiting.

The app keeps its database between tests while the mock is reset, so its
periodic pass also retries earlier tests' repos (404s from the mock in the app
log are expected noise).

## Known gaps

* A repo-level event (pull_request, check_run, merge_group) delivered before
  `installation.created` returns 500 in `internal/github` because GitHub's thin
  installation block leaves `account_type` empty. `TestPullRequestBeforeInstallation`
  documents it and is skipped.
