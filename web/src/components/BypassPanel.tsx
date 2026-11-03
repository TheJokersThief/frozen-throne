import { useState } from "react";
import { ApiError, errorMessage } from "../api/client";
import { useCreateBypass, useDeleteBypass } from "../api/hooks";
import type { Bypass } from "../api/types";
import { formatDateTime } from "../lib/time";
import { useToast } from "./Toast";

/** Exempt a single pull request from the freeze; reason is mandatory. */
export function BypassPanel({
  owner, repo, bypasses, canBypass, frozen,
}: { owner: string; repo: string; bypasses: Bypass[]; canBypass: boolean; frozen: boolean }) {
  const [pr, setPr] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ pr?: string; reason?: string }>({});
  const create = useCreateBypass(), del = useDeleteBypass();
  const toast = useToast();

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const n = Number(pr.replace(/^#/, ""));
    const next: typeof errors = {};
    if (!Number.isInteger(n) || n < 1) next.pr = "Enter a pull request number";
    if (!reason.trim()) next.reason = "A reason is required";
    setErrors(next);
    if (next.pr || next.reason) return;
    create.mutate({ owner, repo, number: n, reason: reason.trim() }, {
      onSuccess: () => { toast(`PR #${n} bypassed`); setPr(""); setReason(""); },
    });
  };
  const serverReason = create.error instanceof ApiError ? create.error.fields?.reason : undefined;

  return (
    <section className="card" aria-labelledby="bypass-h">
      <h2 id="bypass-h">Bypasses</h2>
      <p className="muted small">
        A bypass exempts one pull request (not a commit). It survives new pushes and ends when the freeze ends or the PR merges or closes.
      </p>
      {bypasses.length === 0 ? <p className="muted">No active bypasses.</p> : (
        <ul className="list">
          {bypasses.map((b) => (
            <li key={b.id}>
              <div className="row between">
                <div>
                  <strong>PR #{b.pr_number}</strong>
                  <div className="small">{b.reason}</div>
                  <div className="muted small">by {b.created_by}, {formatDateTime(b.created_at)}</div>
                </div>
                {canBypass && (
                  <button className="btn sm danger" aria-label={`Revoke bypass for PR #${b.pr_number}`} disabled={del.isPending}
                    onClick={() => del.mutate({ owner, repo, number: b.pr_number }, { onSuccess: () => toast(`Bypass for PR #${b.pr_number} revoked`) })}>
                    Revoke
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {canBypass ? (
        <form onSubmit={submit} noValidate style={{ marginTop: "1rem" }}>
          <h3>Bypass a pull request</h3>
          {!frozen && <p className="muted small">This repo is not frozen right now; the bypass takes effect when a freeze starts.</p>}
          <div className="row" style={{ alignItems: "flex-start" }}>
            <div className="field" style={{ width: "9rem" }}>
              <label htmlFor="bp-pr">PR number</label>
              <input id="bp-pr" inputMode="numeric" value={pr} onChange={(e) => setPr(e.target.value)}
                aria-invalid={errors.pr ? true : undefined} aria-describedby={errors.pr ? "bp-pr-err" : undefined} placeholder="#123" />
              {errors.pr && <span className="error" id="bp-pr-err" role="alert">{errors.pr}</span>}
            </div>
            <div className="field" style={{ flex: "1 1 14rem" }}>
              <label htmlFor="bp-reason">Reason</label>
              <input id="bp-reason" maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)}
                aria-invalid={errors.reason || serverReason ? true : undefined} aria-describedby={errors.reason ? "bp-reason-err" : undefined}
                placeholder="Why is this PR exempt?" />
              {(errors.reason || serverReason) && <span className="error" id="bp-reason-err" role="alert">{errors.reason ?? serverReason}</span>}
            </div>
          </div>
          {create.isError && !serverReason && <p className="error" role="alert">{errorMessage(create.error)}</p>}
          <button className="btn primary" type="submit" disabled={create.isPending}>Bypass PR</button>
        </form>
      ) : (
        <p className="muted small">You need maintainer access to bypass pull requests.</p>
      )}
    </section>
  );
}
