import { Link, useParams } from "react-router-dom";
import { ApiError, errorMessage } from "../api/client";
import { useAudit, useRepo, useSetPublicStatus } from "../api/hooks";
import { BypassPanel } from "../components/BypassPanel";
import { FreezeControl } from "../components/FreezeControl";
import { ScheduleManager } from "../components/ScheduleManager";
import { ReasonList, StatusBadge } from "../components/Status";
import { AuditList } from "./AuditPage";
import { formatDateTime } from "../lib/time";

export function RepoPage() {
  const { owner = "", repo = "" } = useParams();
  const q = useRepo(owner, repo);
  const publicStatus = useSetPublicStatus();
  const audit = useAudit({ org: "", repo: `${owner}/${repo}`, actor: "", action: "" });

  if (q.isLoading) return <p className="empty" role="status">Loading…</p>;
  if (q.isError) {
    const notFound = q.error instanceof ApiError && q.error.status === 404;
    return (
      <p className="notice error" role="alert">
        {notFound ? "Repository not found, or you do not have access." : errorMessage(q.error)}{" "}
        <Link to="/">Back to repositories</Link>
      </p>
    );
  }
  const r = q.data!;
  const p = r.permissions;
  const repoScoped = (r.schedules ?? []).filter((s) => s.scope === "repo");
  const orgWide = (r.schedules ?? []).filter((s) => s.scope === "org");
  const own = r.state.reasons.some((x) => x.kind === "repo_freeze");

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/">Repositories</Link> / <Link to={`/orgs/${r.owner}`}>{r.owner}</Link>
      </nav>
      <div className={`card${r.state.frozen ? " frozen" : ""}`}>
        <div className="page-head" style={{ marginBottom: 0 }}>
          <div>
            <h1>{r.full_name}</h1>
            <div className="row">
              <StatusBadge state={r.state} big />
              <span className="muted">
                {r.open_prs ?? 0} open PR{r.open_prs === 1 ? "" : "s"}{r.state.frozen ? ` · ${r.blocked_prs ?? 0} blocked` : ""}
              </span>
            </div>
          </div>
          <div>
            <FreezeControl size="big" target={{ kind: "repo", owner: r.owner, repo: r.name }} state={r.state}
              canFreeze={!!p?.can_freeze} canThaw={!!p?.can_thaw} />
            {!(own ? p?.can_thaw : p?.can_freeze) && <p className="muted small">Maintainer access is needed to {own ? "thaw" : "freeze"}.</p>}
          </div>
        </div>
        {r.state.frozen ? (
          <div style={{ marginTop: "1rem" }}>
            <h2>Why it is frozen</h2>
            <ReasonList reasons={r.state.reasons} />
            {!own && <p className="muted small">The repo itself has no freeze; thaw the cause above to open it.</p>}
          </div>
        ) : r.state.next_change ? (
          <p className="muted" style={{ marginTop: ".75rem" }}>Next scheduled change: {formatDateTime(r.state.next_change)}</p>
        ) : null}
      </div>

      <div className="grid-2">
        <BypassPanel owner={r.owner} repo={r.name} bypasses={r.bypasses ?? []} canBypass={!!p?.can_bypass} frozen={r.state.frozen} />
        <div>
          <ScheduleManager title="Freeze windows" target={{ owner: r.owner, repo: r.name }} schedules={repoScoped}
            canManage={!!p?.can_manage_schedules} emptyText="No windows for this repo." />
          {orgWide.length > 0 && (
            <ScheduleManager title="Org-wide windows" target={{ org: r.owner }} schedules={orgWide} canManage={false} />
          )}
        </div>
      </div>

      {p?.can_freeze && (
        <section className="card">
          <h2>Public status</h2>
          <label className="row">
            <input type="checkbox" checked={!!r.public_status} disabled={publicStatus.isPending}
              onChange={(e) => publicStatus.mutate({ owner: r.owner, repo: r.name, enabled: e.target.checked })} />
            Expose freeze status and badge publicly (no reasons or PR data)
          </label>
          {r.public_status && (
            <p className="small mono muted">/api/v1/public/repos/{r.full_name}/badge.svg</p>
          )}
        </section>
      )}

      <section className="card" aria-labelledby="repo-audit">
        <div className="row between">
          <h2 id="repo-audit">Recent activity</h2>
          <Link to={`/audit?repo=${encodeURIComponent(r.full_name)}`}>Full log</Link>
        </div>
        {audit.isLoading ? <p className="muted">Loading…</p> : <AuditList entries={(audit.data?.pages[0].items ?? []).slice(0, 5)} />}
      </section>
    </>
  );
}
