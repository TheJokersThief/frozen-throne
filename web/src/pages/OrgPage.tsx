import { Link, useParams } from "react-router-dom";
import { ApiError } from "../api/client";
import { useOrg, useOrgSchedules } from "../api/hooks";
import { ErrorNotice, NotFound } from "../components/ErrorNotice";
import { FreezeControl } from "../components/FreezeControl";
import { ScheduleManager } from "../components/ScheduleManager";
import { ReasonList, StatusBadge } from "../components/Status";

export function OrgPage() {
  const { org = "" } = useParams();
  const o = useOrg(org);
  const schedules = useOrgSchedules(org);
  if (o.isLoading) return <p className="empty" role="status">Loading…</p>;
  if (o.isError) {
    if (o.error instanceof ApiError && o.error.status === 404) return <NotFound title="Organization not found" what="organization" />;
    return <ErrorNotice error={o.error} what="this organization" onRetry={() => void o.refetch()} />;
  }
  const d = o.data!;
  const p = d.permissions;
  const hasOwn = d.state.reasons.some((r) => r.kind === "org_freeze");
  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb"><Link to="/">Repositories</Link> / {d.login}</nav>
      <div className={`card${d.state.frozen ? " frozen" : ""}`}>
        <div className="page-head" style={{ marginBottom: 0 }}>
          <div>
            <h1>{d.login}</h1>
            <div className="row">
              <StatusBadge state={d.state} big />
              <span className="muted">{d.repo_count ?? 0} repositories</span>
            </div>
          </div>
          <div>
            <FreezeControl size="big" target={{ kind: "org", org: d.login }} state={d.state}
              canFreeze={!!p?.can_freeze} canThaw={!!p?.can_thaw} />
            {!(hasOwn ? p?.can_thaw : p?.can_freeze) && <p className="muted small">Org owner access is needed to {hasOwn ? "thaw" : "freeze"} the whole org.</p>}
          </div>
        </div>
        <p className="muted small" style={{ marginTop: ".75rem" }}>
          An org-wide freeze covers every repo, including repos added later. Repos cannot override it.
        </p>
        {d.state.reasons.length > 0 && <ReasonList reasons={d.state.reasons} />}
      </div>
      <ScheduleManager title="Org-wide windows" target={{ org: d.login }} schedules={schedules.data?.items ?? []}
        canManage={!!p?.can_manage_schedules} emptyText="No org-wide windows." />
      <p><Link to={`/?org=${d.login}`}>View repositories</Link> · <Link to={`/audit?org=${d.login}`}>Audit log</Link></p>
    </>
  );
}
