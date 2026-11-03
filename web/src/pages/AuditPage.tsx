import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { errorMessage } from "../api/client";
import { useAudit, useOrgs } from "../api/hooks";
import type { AuditAction, AuditEntry } from "../api/types";
import { formatDateTime } from "../lib/time";

const ACTIONS: Record<AuditAction, string> = {
  freeze: "Froze", thaw: "Thawed", "schedule.create": "Created window", "schedule.update": "Updated window",
  "schedule.delete": "Deleted window", "bypass.create": "Bypassed PR", "bypass.end": "Bypass ended",
  "slack.update": "Updated Slack", "public_status.update": "Changed public status",
};

export function AuditList({ entries }: { entries: AuditEntry[] }) {
  if (!entries.length) return <p className="muted">No activity yet.</p>;
  return (
    <ul className="list">
      {entries.map((e) => (
        <li key={e.id} className="audit-item">
          <time className="muted small" dateTime={e.created_at}>{formatDateTime(e.created_at)}</time>
          <div>
            <strong>{ACTIONS[e.action]}</strong>{" "}
            {e.repo ? <Link to={`/repos/${e.repo}`}>{e.repo}</Link> : <>org <Link to={`/orgs/${e.org}`}>{e.org}</Link></>}
            {e.pr_number != null && <> · PR #{e.pr_number}</>}
            <span className="muted"> — {e.actor ? `by ${e.actor}` : "automatically"}</span>
            {e.reason && <div className="small">“{e.reason}”</div>}
            {typeof e.details?.name === "string" && <div className="muted small">{e.details.name}</div>}
          </div>
        </li>
      ))}
    </ul>
  );
}

export function AuditPage() {
  const [sp, setSp] = useSearchParams();
  const [actor, setActor] = useState(sp.get("actor") ?? "");
  const org = sp.get("org") ?? "", repo = sp.get("repo") ?? "";
  const action = (sp.get("action") ?? "") as "" | AuditAction;
  const orgs = useOrgs();
  const q = useAudit({ org, repo, actor: sp.get("actor") ?? "", action });
  const set = (k: string, v: string) => {
    const n = new URLSearchParams(sp);
    if (v) n.set(k, v); else n.delete(k);
    setSp(n, { replace: true });
  };
  const entries = q.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <h1>Audit log</h1>
      <p className="muted">Who froze, thawed, scheduled or bypassed — newest first.</p>
      <form className="toolbar" role="search" onSubmit={(e) => { e.preventDefault(); set("actor", actor.trim()); }}>
        <label className="sr-only" htmlFor="a-org">Organization</label>
        <select id="a-org" value={org} onChange={(e) => set("org", e.target.value)}>
          <option value="">All organizations</option>
          {orgs.data?.items.map((o) => <option key={o.login} value={o.login}>{o.login}</option>)}
        </select>
        <label className="sr-only" htmlFor="a-action">Action</label>
        <select id="a-action" value={action} onChange={(e) => set("action", e.target.value)}>
          <option value="">All actions</option>
          {(Object.keys(ACTIONS) as AuditAction[]).map((a) => <option key={a} value={a}>{ACTIONS[a]}</option>)}
        </select>
        <label className="sr-only" htmlFor="a-actor">Actor</label>
        <input id="a-actor" type="search" placeholder="Actor login" value={actor} onChange={(e) => setActor(e.target.value)} />
        <button className="btn sm" type="submit">Filter</button>
        {repo && <span className="chip">{repo} <button type="button" className="btn link" aria-label="Clear repo filter" onClick={() => set("repo", "")}>×</button></span>}
      </form>
      {q.isError ? <p className="notice error" role="alert">{errorMessage(q.error)}</p>
        : q.isLoading ? <p className="empty" role="status">Loading…</p>
        : (
          <div className="card">
            <AuditList entries={entries} />
            {q.hasNextPage && (
              <div className="pager">
                <button className="btn" disabled={q.isFetchingNextPage} onClick={() => q.fetchNextPage()}>
                  {q.isFetchingNextPage ? "Loading…" : "Load older entries"}
                </button>
              </div>
            )}
          </div>
        )}
    </>
  );
}
