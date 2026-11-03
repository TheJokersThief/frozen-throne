# frozen-throne v2: design

## Intent

frozen-throne is merge-freeze tooling for GitHub. A repo (or a whole org) can be
frozen, and while it is frozen no pull request can merge. v2 is a fresh rewrite
(reusing v1's ideas, not its code) built to be **self-hosted by other people**
and open source.

Why v2: v1's blocking was unreliable, specifically **stale state**. After a
freeze was toggled, existing PRs kept an old pass/fail result. v2's central
promise is that the check on every open PR always matches the current freeze
state, even if webhooks are missed.

## Goals

- Block merges while frozen, using a GitHub App that posts a Check Run on each
  PR (fail while frozen, pass otherwise). Admins make it a required check in
  branch protection.
- Never leave stale state: on any freeze, thaw or schedule change, re-evaluate
  every open PR in the affected repos. A periodic reconcile loop catches missed
  webhooks. Handle the `check_run` `rerequested` event.
- Freeze scopes: single repo, org-wide, and scheduled windows (one-off and
  recurring, with timezone).
- Per-PR bypass: authorized users can exempt one PR, with a reason, recorded in
  the audit log.
- A simple, great web app: a dashboard showing all repos at a glance, with big
  freeze/thaw controls, status and search.
- Audit log (who froze, thawed or bypassed, when, why), Slack notifications, and
  a public read-only status endpoint/badge.
- Easy to self-host: one Docker image, or one static binary with the web assets
  embedded.

## Non-goals

- Multi-tenant SaaS operation, billing, or hosting for others.
- Per-branch freeze scoping (freezes apply to a whole repo; may come later).
- Local user accounts; Postgres support.
- Helm/Kubernetes manifests.

## Anti-goals

- A freeze that appears active in the UI but doesn't actually block PRs.
- A thaw that leaves PRs red.
- Setup so involved that self-hosters give up (keep to one binary/container plus
  a GitHub App).

## Decisions (made by the owner)

- Mechanism: GitHub App + Check Runs.
- Stack: Go backend, React + TypeScript web app, SQLite storage.
- Auth: GitHub OAuth via the App. Permissions follow GitHub: repo
  admins/maintainers may freeze their repos; org owners may freeze org-wide.
  (The exact role-to-permission mapping is the builder's call within this rule.)
- Reconcile strategy: re-evaluate all open PRs on change, plus a periodic loop.
- Schedules: one-off and recurring, timezone-aware.
- Deploy: Docker image and static binary (web assets embedded).
- UI: dashboard of all repos at a glance.
- Rewrite: fresh structure in this repo; v1 code removed as v2 lands.

- Precedence: a repo is frozen if ANY of these applies: an org-wide freeze, a
  repo freeze, or an active schedule window. Thawing one does not cancel the
  others. Windows auto-thaw when they end. No repo-level override of an org
  freeze.
- Bypass: exempts one PR (not one commit). A reason is required. It survives new
  pushes and is cleared when the freeze ends or the PR merges or closes.
- Who may act: the same people who may freeze (repo admins/maintainers for their
  repos, org owners for org-wide) may also thaw any freeze and bypass any PR.
- Public status endpoint/badge: opt-in per repo. Nothing about unlisted repos
  (names, reasons) is ever exposed.

## Calls made by the orchestrator (please check these)

- One deployment can serve several GitHub installations/orgs. The app tracks
  installations via `installation` and `installation_repositories` events, and
  an org-wide freeze also covers repos added later.
- Permissions are checked live against the GitHub API, with a short cache
  (about 1 minute), so losing admin takes effect quickly.
- The check covers PRs into every branch, including drafts and forks, and also
  reports on `merge_group` events so merge queues don't stall.
- A publicly reachable URL is required for webhooks and OAuth. If webhooks are
  missed, the reconcile loop heals state; there is no polling-only mode.
- Sizing target: a single instance (no HA) with up to a few hundred repos and a
  few thousand open PRs; reconcile must respect GitHub rate limits.
- Secrets (App private key, webhook secret, OAuth secret, session secret) are
  supplied via environment variables or files. Sessions use signed cookies and
  the web app is served from the same origin as the API.

## Left to the builder

- Package layout, router/ORM choices, frontend component library and styling,
  migration tooling, and the exact REST shapes (as long as they are captured in
  the OpenAPI spec in Wave 0).
- Reconcile interval and rate-limit handling against the GitHub API.
- Details of the bypass UX and the badge design.

## Delivery plan: tasks and dependencies

Each item is one task and one PR. Later tasks are created only after their
dependencies have merged.

**Wave 0: foundation (serial)**
- 0. Scaffold + contracts: Go module and web skeleton, CI, removal of v1 code,
  SQLite schema and migrations, Go store interfaces, and the OpenAPI spec.
  Everything else builds against these contracts, which is what allows wave 1 to
  run in parallel without touching the same files.

**Wave 1: parallel, separate directories (needs 0)**
- A. Freeze engine: pure library for repo/org freezes, one-off and recurring
  schedules with timezones, and bypass evaluation.
- B. GitHub App: webhook receiver with signature verification, check-run
  create/update, built on the store interfaces.
- C. API server: handlers from the OpenAPI spec, GitHub OAuth, permission
  checks.
- D. Web dashboard: built against the OpenAPI spec with a mocked API.

**Wave 2 (needs wave 1)**
- E. Reconcile: re-evaluate open PRs on change plus periodic loop (needs A, B).
- F. Audit log and per-PR bypass, end to end (needs A, C).
- G. Wire the dashboard to the real API (needs C, D).

**Wave 3 (needs wave 2)**
- H. Slack notifications.
- I. Public status endpoint and badge.
- J. Packaging and release: Docker image, static binary with embedded web assets,
  setup/docs guide.
- K. End-to-end test against a fake GitHub.

## Success

A self-hoster can deploy one container, install the GitHub App, mark the check
as required, freeze a repo from the dashboard, and see every open PR turn red
within moments. Thawing turns them green. Missed webhooks heal on the next
reconcile.
