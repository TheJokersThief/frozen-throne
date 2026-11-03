import { useDeferredValue, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { errorMessage } from "../api/client";
import { useOrgs, useRepos } from "../api/hooks";
import type { Org, RepoSummary } from "../api/types";
import { FreezeControl } from "../components/FreezeControl";
import { ReasonChips, StatusBadge } from "../components/Status";

const PAGE = 25;
type StateFilter = "" | "frozen" | "thawed";

export function Dashboard() {
  const [q, setQ] = useState("");
  const [sp] = useSearchParams();
  const [org, setOrg] = useState(sp.get("org") ?? "");
  const [state, setState] = useState<StateFilter>("");
  const [page, setPage] = useState(0);
  const dq = useDeferredValue(q);
  const orgs = useOrgs();
  const repos = useRepos({ q: dq.trim(), org, state, offset: page * PAGE, limit: PAGE });
  const reset = <T,>(set: (v: T) => void) => (v: T) => { set(v); setPage(0); };
  const total = repos.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE));

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Repositories</h1>
          <p className="muted">Merge freeze status across everything you can see.</p>
        </div>
      </div>

      {orgs.data && orgs.data.items.length > 0 && (
        <section className="org-strip" aria-label="Organizations">
          {orgs.data.items.map((o) => <OrgCard key={o.installation_id} org={o} />)}
        </section>
      )}

      <div className="toolbar" role="search">
        <label className="sr-only" htmlFor="repo-search">Search repositories</label>
        <input id="repo-search" type="search" placeholder="Search repositories…" value={q}
          onChange={(e) => reset(setQ)(e.target.value)} />
        <label className="sr-only" htmlFor="org-filter">Organization</label>
        <select id="org-filter" value={org} onChange={(e) => reset(setOrg)(e.target.value)}>
          <option value="">All organizations</option>
          {orgs.data?.items.map((o) => <option key={o.login} value={o.login}>{o.login}</option>)}
        </select>
        <div className="seg" role="group" aria-label="Freeze state">
          {([["", "All"], ["frozen", "Frozen"], ["thawed", "Open"]] as const).map(([v, label]) => (
            <button key={v} type="button" aria-pressed={state === v} onClick={() => reset(setState)(v)}>{label}</button>
          ))}
        </div>
      </div>

      {repos.isError ? (
        <p className="notice error" role="alert">Could not load repositories: {errorMessage(repos.error)}</p>
      ) : repos.isLoading ? (
        <p className="empty" role="status">Loading repositories…</p>
      ) : repos.data!.items.length === 0 ? (
        <p className="empty">No repositories match.</p>
      ) : (
        <>
          <p className="muted small" aria-live="polite">{total} {total === 1 ? "repository" : "repositories"}</p>
          <ul className="repo-list" aria-label="Repositories">
            {repos.data!.items.map((r) => <RepoRow key={r.id} repo={r} />)}
          </ul>
          {pages > 1 && (
            <nav className="pager" aria-label="Pagination">
              <button className="btn sm" disabled={page === 0} onClick={() => setPage(page - 1)}>Previous</button>
              <span>Page {page + 1} of {pages}</span>
              <button className="btn sm" disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}>Next</button>
            </nav>
          )}
        </>
      )}
    </>
  );
}

function OrgCard({ org }: { org: Org }) {
  const p = org.permissions;
  return (
    <div className={`org-card${org.state.frozen ? " frozen" : ""}`}>
      <div className="row between">
        <Link to={`/orgs/${org.login}`}><strong>{org.login}</strong></Link>
        <StatusBadge state={org.state} />
      </div>
      <div className="muted small">{org.repo_count ?? 0} repos · org-wide freeze {org.state.frozen ? "active" : "off"}</div>
      <div>
        <FreezeControl target={{ kind: "org", org: org.login }} state={org.state}
          canFreeze={!!p?.can_freeze} canThaw={!!p?.can_thaw} />
      </div>
    </div>
  );
}

function RepoRow({ repo }: { repo: RepoSummary }) {
  const p = repo.permissions;
  const own = repo.state.reasons.some((r) => r.kind === "repo_freeze");
  const onlyInherited = repo.state.frozen && !own;
  return (
    <li className={`repo${repo.state.frozen ? " frozen" : ""}`}>
      <div>
        <Link className="repo-name" to={`/repos/${repo.owner}/${repo.name}`}>{repo.full_name}</Link>
        <div className="repo-meta muted small">
          {repo.private && <span className="tag">private</span>}
          {repo.archived && <span className="tag">archived</span>}
          <span>{repo.open_prs ?? 0} open PR{repo.open_prs === 1 ? "" : "s"}{repo.state.frozen ? `, ${repo.blocked_prs ?? 0} blocked` : ""}</span>
        </div>
      </div>
      <div className="repo-meta">
        <StatusBadge state={repo.state} />
        <ReasonChips reasons={repo.state.reasons} />
      </div>
      <div className="actions">
        <FreezeControl target={{ kind: "repo", owner: repo.owner, repo: repo.name }} state={repo.state}
          canFreeze={!!p?.can_freeze} canThaw={!!p?.can_thaw} />
        {onlyInherited && !p?.can_freeze && null}
      </div>
    </li>
  );
}
