import type { FreezeReason, FreezeState } from "../api/types";
import { formatDateTime } from "../lib/time";

export function StatusBadge({ state, big }: { state: FreezeState; big?: boolean }) {
  const frozen = state.frozen;
  return (
    <span className={`badge ${frozen ? "frozen" : "thawed"}${big ? " big" : ""}`}>
      <span aria-hidden="true">{frozen ? "❄" : "✓"}</span>
      {frozen ? "Frozen" : "Open"}
    </span>
  );
}

export function reasonLabel(r: FreezeReason): string {
  switch (r.kind) {
    case "org_freeze": return "Org freeze";
    case "repo_freeze": return "Repo freeze";
    case "schedule": return "Window";
  }
}

export function ReasonChips({ reasons }: { reasons: FreezeReason[] }) {
  if (!reasons.length) return null;
  return (
    <ul className="chips" style={{ listStyle: "none", margin: 0, padding: 0 }} aria-label="Why it is frozen">
      {reasons.map((r, i) => (
        <li className="chip" key={i} title={r.text}>
          <strong>{reasonLabel(r)}</strong>
          {r.text && <span className="muted">{r.text.length > 36 ? r.text.slice(0, 35) + "…" : r.text}</span>}
        </li>
      ))}
    </ul>
  );
}

/** Detailed explanation of each cause, for the repo / org pages. */
export function ReasonList({ reasons }: { reasons: FreezeReason[] }) {
  return (
    <ul className="list">
      {reasons.map((r, i) => (
        <li key={i}>
          <strong>{reasonLabel(r)}</strong>
          {r.text && <> — {r.text}</>}
          <div className="muted small">
            {r.by ? `by ${r.by}, ` : ""}since {formatDateTime(r.since)}
            {r.until ? ` · thaws automatically ${formatDateTime(r.until)}` : ""}
          </div>
        </li>
      ))}
    </ul>
  );
}
