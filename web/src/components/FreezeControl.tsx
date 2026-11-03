import { useState } from "react";
import { errorMessage } from "../api/client";
import { useFreezeOrg, useFreezeRepo, useThawOrg, useThawRepo } from "../api/hooks";
import type { FreezeState } from "../api/types";
import { Modal } from "./Modal";
import { reasonLabel } from "./Status";
import { useToast } from "./Toast";

export type Target = { kind: "repo"; owner: string; repo: string } | { kind: "org"; org: string };

const nameOf = (t: Target) => (t.kind === "repo" ? `${t.owner}/${t.repo}` : t.org);

/**
 * Freeze / thaw button with a confirm dialog. Freeze asks for a reason;
 * thaw lists any other causes that will keep the repo frozen.
 */
export function FreezeControl({
  target, state, canFreeze, canThaw, size = "sm",
}: { target: Target; state: FreezeState; canFreeze: boolean; canThaw: boolean; size?: "sm" | "big" }) {
  const [open, setOpen] = useState(false);
  const own = state.reasons.some((r) => r.kind === (target.kind === "repo" ? "repo_freeze" : "org_freeze"));
  if (own ? !canThaw : !canFreeze) return null;
  const cls = size === "big" ? "btn big" : "btn sm";
  return (
    <>
      <button type="button" className={`${cls} ${own ? "thaw" : "freeze"}`} onClick={() => setOpen(true)}
        aria-label={`${own ? "Thaw" : "Freeze"} ${nameOf(target)}`}>
        {own ? (target.kind === "org" ? "Thaw org" : "Thaw") : target.kind === "org" ? "Freeze org" : "Freeze"}
      </button>
      {open && <FreezeDialog target={target} state={state} thaw={own} onClose={() => setOpen(false)} />}
    </>
  );
}

function FreezeDialog({ target, state, thaw, onClose }: { target: Target; state: FreezeState; thaw: boolean; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const toast = useToast();
  const freezeRepo = useFreezeRepo(), thawRepo = useThawRepo(), freezeOrg = useFreezeOrg(), thawOrg = useThawOrg();
  const m = target.kind === "repo" ? (thaw ? thawRepo : freezeRepo) : thaw ? thawOrg : freezeOrg;
  const name = nameOf(target);
  const others = state.reasons.filter((r) => r.kind !== (target.kind === "repo" ? "repo_freeze" : "org_freeze"));

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const done = (res: { rechecking?: number }) => {
      const n = res.rechecking ? ` — rechecking ${res.rechecking} open PR${res.rechecking === 1 ? "" : "s"}` : "";
      toast(`${thaw ? "Thawed" : "Frozen"} ${name}${n}`);
      onClose();
    };
    const opts = { onSuccess: done };
    if (target.kind === "repo") {
      const v = { owner: target.owner, repo: target.repo };
      if (thaw) thawRepo.mutate(v, opts); else freezeRepo.mutate({ ...v, reason: reason.trim() }, opts);
    } else if (thaw) thawOrg.mutate({ org: target.org }, opts);
    else freezeOrg.mutate({ org: target.org, reason: reason.trim() }, opts);
  };

  return (
    <Modal title={`${thaw ? "Thaw" : "Freeze"} ${target.kind === "org" ? `all of ${name}` : name}?`} onClose={onClose}>
      <form onSubmit={submit}>
        {thaw ? (
          <>
            <p>Ends the {target.kind} freeze and re-checks every open pull request.</p>
            {others.length > 0 && (
              <p className="notice" role="note">
                {target.kind === "repo" ? "This repo" : "Repos of this org"} will stay frozen
                by: {[...new Set(others.map(reasonLabel))].join(", ")}.
              </p>
            )}
          </>
        ) : (
          <>
            <p>
              {target.kind === "org"
                ? "Blocks merging in every repo of this org, including repos added later."
                : "Fails the merge check on every open pull request in this repo."}{" "}
              Individual PRs can still be bypassed.
            </p>
            <div className="field">
              <label htmlFor="freeze-reason">Reason (shown on the check and in the audit log)</label>
              <textarea id="freeze-reason" rows={3} maxLength={500} value={reason}
                onChange={(e) => setReason(e.target.value)} placeholder="e.g. Release cut, incident, holiday" />
            </div>
          </>
        )}
        {m.isError && <p className="error" role="alert">{errorMessage(m.error)}</p>}
        <div className="modal-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className={`btn ${thaw ? "thaw" : "freeze"}`} disabled={m.isPending}>
            {m.isPending ? "Working…" : thaw ? "Thaw" : "Freeze"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
