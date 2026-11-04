# Configuration

frozen-throne is configured entirely through environment variables. Every
secret can instead be read from a file by setting `<NAME>_FILE` to a path
(Docker/Kubernetes secrets); the file's trailing newline is stripped.

The server refuses to start and lists **every** missing or invalid setting at
once, so a first run is one round trip.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `FT_BASE_URL` | yes | | Public origin of the server, e.g. `https://freeze.example.com` (no path). Used to build the OAuth callback URL. An `https://` value turns on `Secure` cookies; use `http://` only for local trials. |
| `FT_GITHUB_APP_ID` | yes | | Numeric App ID from the GitHub App settings page. |
| `FT_GITHUB_PRIVATE_KEY` / `_FILE` | yes | | The App's private key (PEM, PKCS#1 or PKCS#8). In `FT_GITHUB_PRIVATE_KEY` a single line with literal `\n` sequences is accepted. Prefer `FT_GITHUB_PRIVATE_KEY_FILE`. |
| `FT_GITHUB_WEBHOOK_SECRET` / `_FILE` | yes | | The webhook secret you set on the App. Deliveries with a bad signature are rejected with 401. |
| `FT_GITHUB_CLIENT_ID` | yes | | The App's OAuth client ID (used for "Log in with GitHub"). |
| `FT_GITHUB_CLIENT_SECRET` / `_FILE` | yes | | The App's OAuth client secret. |
| `FT_SESSION_SECRET` / `_FILE` | yes | | At least 32 characters of randomness (`openssl rand -hex 32`). Signs session cookies and encrypts stored GitHub user tokens. Changing it logs everyone out. |
| `FT_DB_PATH` | no | `frozen-throne.db` (`/data/frozen-throne.db` in the image) | SQLite file. Created and migrated on start. Keep it on a persistent volume. |
| `FT_LISTEN_ADDR` | no | `:8080` | Address to listen on. TLS is not terminated here; use a reverse proxy. |
| `FT_RECONCILE_INTERVAL` | no | `5m` | How often every repo's check is re-evaluated to heal missed webhooks (Go duration, e.g. `30s`, `10m`; negative disables). |

## HTTP endpoints

All served from the one origin in `FT_BASE_URL`:

| Path | Purpose |
|------|---------|
| `/` | Dashboard (embedded; client-side routes fall back to `index.html`). |
| `/api/v1/...` | REST API ([`api/openapi.yaml`](../api/openapi.yaml)). |
| `/api/v1/auth/callback` | OAuth callback URL to register on the GitHub App. |
| `/webhooks/github` | Webhook URL to register on the GitHub App. |
| `/healthz` | Liveness probe (`{"status":"ok"}`). |

## Slack

Slack notifications are not configured through environment variables. Each
organization stores its own Slack incoming-webhook URL from the dashboard
(org page, owners only), and it is write-only afterwards: the API never
returns it. No server-side settings are needed.

## Data and backups

All state is the single SQLite file in WAL mode (`FT_DB_PATH`, plus
`-wal`/`-shm` siblings while running). To back up, use
`sqlite3 frozen-throne.db ".backup backup.db"` or snapshot the volume.

## Container details

The image is based on `distroless/static`, runs as a non-root user (uid 65532),
declares `/data` as a volume, exposes 8080, and has a `HEALTHCHECK` that runs
`frozen-throne -healthcheck` (probes `/healthz` on the local listener).
Mounted secret files must be readable by uid 65532.
