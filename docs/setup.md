# Self-hosting guide

You need a host that can run a container (or a single binary), a public HTTPS
URL for it, and a GitHub account that owns, or can create apps for, the
organization you want to protect.

Below, `https://freeze.example.com` stands for your server's public URL.

## 1. Create the GitHub App

GitHub → *Settings* (your user, or the organization) → *Developer settings* →
*GitHub Apps* → *New GitHub App*.

| Field | Value |
|-------|-------|
| GitHub App name | anything unique, e.g. `acme-frozen-throne` |
| Homepage URL | `https://freeze.example.com` |
| Callback URL | `https://freeze.example.com/api/v1/auth/callback` |
| Expire user authorization tokens | leave on (default) |
| Request user authorization (OAuth) during installation | off |
| Webhook → Active | on |
| Webhook URL | `https://freeze.example.com/webhooks/github` |
| Webhook secret | a long random string (`openssl rand -hex 32`); you will set it as `FT_GITHUB_WEBHOOK_SECRET` |

**Repository permissions**

| Permission | Access | Why |
|------------|--------|-----|
| Checks | Read & write | Post and update the `frozen-throne` Check Run. |
| Pull requests | Read-only | List open PRs to re-evaluate them; receive PR events. |
| Metadata | Read-only | Mandatory for every App. |

**Organization permissions**

| Permission | Access | Why |
|------------|--------|-----|
| Members | Read-only | Tell org owners (who may freeze the whole org) from members when someone logs in. |

**Subscribe to events**: `Pull request`, `Check run`, `Merge group`.
(`Installation` and `Installation repositories` events are always delivered
to Apps; the server handles them to learn which repos it protects.)

**Where can this GitHub App be installed?** "Only on this account" is fine for
one org; choose "Any account" to serve several.

After creating it, on the App's page:

1. Note the **App ID** → `FT_GITHUB_APP_ID`.
2. Note the **Client ID** → `FT_GITHUB_CLIENT_ID`.
3. *Generate a new client secret* → `FT_GITHUB_CLIENT_SECRET`.
4. *Generate a private key*; a `.pem` file downloads → `FT_GITHUB_PRIVATE_KEY_FILE`.

## 2. Run the server

### Docker Compose (recommended)

```sh
cp docker-compose.example.yml docker-compose.yml
cp .env.example .env
mv ~/Downloads/<app>.private-key.pem ./github-app.private-key.pem
chmod 644 github-app.private-key.pem        # the container runs as uid 65532
$EDITOR .env                                  # fill in the values from step 1
docker compose up -d
docker compose logs -f
```

Images are published to `ghcr.io/thejokersthief/frozen-throne` on every
version tag (`:1.2.3`, `:1.2`, `:latest`; linux/amd64 and linux/arm64).

### Single binary

Download `frozen-throne-<os>-<arch>` from the
[releases page](https://github.com/TheJokersThief/frozen-throne/releases)
(verify with `checksums.txt`). It is static and embeds the dashboard.

```sh
export FT_BASE_URL=https://freeze.example.com FT_GITHUB_APP_ID=... \
  FT_GITHUB_PRIVATE_KEY_FILE=/etc/frozen-throne/key.pem \
  FT_GITHUB_WEBHOOK_SECRET=... FT_GITHUB_CLIENT_ID=... \
  FT_GITHUB_CLIENT_SECRET=... FT_SESSION_SECRET=$(openssl rand -hex 32) \
  FT_DB_PATH=/var/lib/frozen-throne/frozen-throne.db
./frozen-throne-linux-amd64
```

Run it under systemd or similar. All variables are described in
[configuration.md](configuration.md).

### Put HTTPS in front

The server speaks plain HTTP on `:8080`. Terminate TLS with a reverse proxy
(Caddy, nginx, Traefik, a cloud load balancer) at the URL you configured in
`FT_BASE_URL`, forwarding everything (`/`, `/api/`, `/webhooks/github`) to it.
GitHub must be able to reach `/webhooks/github` from the internet.

Verify:

```sh
curl https://freeze.example.com/healthz     # {"status":"ok"}
```

## 3. Install the App

On the App's page choose *Install App*, pick the organization, and select
all repositories or just the ones to protect. As soon as the installation
webhook arrives the server registers the repos and posts a Check Run named
**`frozen-throne`** on every open PR (green while thawed).

In the GitHub App's *Advanced* tab, *Recent Deliveries* should show `200`
responses.

## 4. Require the check in branch protection

A freeze only blocks merging if the check is required.

- **Branch protection rule** (or ruleset) on your default branch → *Require
  status checks to pass before merging* → search for and add
  **`frozen-throne`** (it appears once the App has posted it on a PR) →
  *Save*.
- With **merge queues**, add the check there too; the server also reports on
  `merge_group` events.
- Do not tick "Do not allow bypassing the above settings" unless you want
  admins unable to merge during a freeze. Use frozen-throne's per-PR bypass
  (with a recorded reason) instead.

## 5. First login

Open `https://freeze.example.com`, click *Log in with GitHub*, and approve.
You see repos in the installations where you have access. Repo
admins/maintainers can freeze, thaw, schedule and bypass for their repos;
organization owners can also freeze the whole org and edit org-wide windows.
Permissions are checked live against GitHub with your own token, so removing
someone on GitHub removes their access here.

Try it: freeze a test repo, open a PR, confirm the `frozen-throne` check
fails with "Frozen", thaw it, confirm it turns green.

## Semantics in one paragraph

A repo is frozen if **any** of: its org is frozen, the repo is frozen, or a
schedule window (one-off or recurring) is active. Windows thaw automatically
at their end. A repo cannot override an org freeze. A bypass exempts one PR
(a reason is required), survives new pushes, and is cleared when the freeze
ends or the PR merges or closes.

## Upgrading

Pull the new image tag (or replace the binary) and restart. Database
migrations are embedded and applied automatically on start; they are
forward-only, so back up `FT_DB_PATH` first.

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| Container exits immediately with `configuration:` and a list | Missing/invalid environment variables; the list names each one. |
| `FT_GITHUB_PRIVATE_KEY: github: private key is not PEM encoded` | The key was truncated or mangled. Use `FT_GITHUB_PRIVATE_KEY_FILE`; with `_KEY`, keep real newlines or `\n` escapes. |
| `permission denied` reading the key file / opening the DB | The container runs as uid 65532. Make the key file readable and the data volume writable by it. |
| Webhook deliveries show `401 invalid signature` | `FT_GITHUB_WEBHOOK_SECRET` differs from the App's webhook secret. Fix and *Redeliver*. |
| Webhook deliveries time out / `502` | The proxy cannot reach the container, or `/webhooks/github` is not forwarded. |
| No `frozen-throne` check appears on PRs | App not installed on that repo, *Checks: write* missing, or the `Pull request` event is not subscribed. Check *Recent Deliveries*, then the server log. The periodic reconcile (`FT_RECONCILE_INTERVAL`) repairs missed deliveries. |
| Check is not offered in branch protection | GitHub lists checks only after they ran in the last week. Open a PR first. |
| Login: "redirect_uri is not associated with this application" | The App's *Callback URL* must be exactly `FT_BASE_URL` + `/api/v1/auth/callback`. |
| Login succeeds but you are logged out immediately | `FT_BASE_URL` is `https://` but you browse over `http://` (Secure cookie), or `FT_SESSION_SECRET` changed. |
| Dashboard empty after login | You have no access to repos in an installation, or the App is not installed on any repo yet. |
| Cannot freeze the whole org | Only organization owners can; the Members: read permission must be granted (accept the updated permissions request on the installation if you added it later). |
| Dashboard 404s on reload | A proxy is serving static files itself. Forward all paths to the server; it handles client-side routes. |
| `web UI not built into this binary` (503 on `/`) | You built from source without `just embed-web`. Use a release binary or image. |

Logs go to stderr in `key=value` form. Health: `GET /healthz`, or
`docker inspect --format '{{.State.Health.Status}}' <container>`.

## Building from source

```sh
just release-build     # dist/frozen-throne-{linux,darwin}-{amd64,arm64}
just image             # docker build -t frozen-throne:dev .
```
