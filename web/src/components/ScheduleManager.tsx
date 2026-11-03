import { useState } from "react";
import { errorMessage } from "../api/client";
import { useCreateSchedule, useDeleteSchedule, useUpdateSchedule, type ScheduleTarget } from "../api/hooks";
import type { Schedule } from "../api/types";
import { WEEKDAY_LABEL, formatDateTime } from "../lib/time";
import { Modal } from "./Modal";
import { ScheduleForm } from "./ScheduleForm";
import { useToast } from "./Toast";

export function describeSchedule(s: Schedule): string {
  if (s.kind === "one_off") {
    return `${s.starts_at ? formatDateTime(s.starts_at, s.timezone) : "?"} → ${s.ends_at ? formatDateTime(s.ends_at, s.timezone) : "?"} (${s.timezone})`;
  }
  const days = (s.weekdays ?? []).map((d) => WEEKDAY_LABEL[d]).join(", ");
  return `${days} ${s.start_time}–${s.end_time} (${s.timezone})`;
}

/** Lists freeze windows with create / edit / delete. `canManage` gates the controls. */
export function ScheduleManager({
  title, target, schedules, canManage, emptyText = "No windows yet.",
}: {
  title: string; target: ScheduleTarget; schedules: Schedule[]; canManage: boolean; emptyText?: string;
}) {
  const [editing, setEditing] = useState<Schedule | "new" | null>(null);
  const [deleting, setDeleting] = useState<Schedule | null>(null);
  const create = useCreateSchedule(), update = useUpdateSchedule(), del = useDeleteSchedule();
  const toast = useToast();
  const close = () => { setEditing(null); create.reset(); update.reset(); };

  return (
    <section className="card" aria-labelledby={`sched-${title}`}>
      <div className="row between">
        <h2 id={`sched-${title}`}>{title}</h2>
        {canManage && <button className="btn sm" onClick={() => setEditing("new")}>New window</button>}
      </div>
      {schedules.length === 0 ? <p className="muted">{emptyText}</p> : (
        <ul className="list">
          {schedules.map((s) => (
            <li key={s.id}>
              <div className="row between">
                <div>
                  <strong>{s.name || "Untitled window"}</strong>{" "}
                  <span className="tag">{s.kind === "one_off" ? "one-off" : "recurring"}</span>{" "}
                  {s.active && <span className="badge frozen">❄ Active now</span>}
                  {!s.enabled && <span className="tag">disabled</span>}
                  <div className="muted small">{describeSchedule(s)}</div>
                  {s.reason && <div className="small">{s.reason}</div>}
                  {s.next_start && !s.active && <div className="muted small">Next: {formatDateTime(s.next_start)}</div>}
                </div>
                {canManage && (
                  <div className="row">
                    <button className="btn sm" onClick={() => setEditing(s)} aria-label={`Edit ${s.name || "window"}`}>Edit</button>
                    <button className="btn sm danger" onClick={() => setDeleting(s)} aria-label={`Delete ${s.name || "window"}`}>Delete</button>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <Modal wide title={editing === "new" ? "New freeze window" : "Edit freeze window"} onClose={close}>
          <ScheduleForm
            initial={editing === "new" ? undefined : editing}
            pending={create.isPending || update.isPending}
            error={create.error ?? update.error}
            onCancel={close}
            onSubmit={(input) => {
              const done = { onSuccess: () => { toast(editing === "new" ? "Window created" : "Window saved"); close(); } };
              if (editing === "new") create.mutate({ target, input }, done);
              else update.mutate({ id: editing.id, input }, done);
            }}
          />
        </Modal>
      )}
      {deleting && (
        <Modal title={`Delete “${deleting.name || "window"}”?`} onClose={() => setDeleting(null)}>
          <p>{deleting.active ? "This window is active; deleting it thaws it immediately (other causes may still freeze the repo)." : "The window will no longer freeze anything."}</p>
          {del.isError && <p className="error" role="alert">{errorMessage(del.error)}</p>}
          <div className="modal-actions">
            <button className="btn" onClick={() => setDeleting(null)}>Cancel</button>
            <button className="btn primary" disabled={del.isPending}
              onClick={() => del.mutate({ id: deleting.id }, { onSuccess: () => { toast("Window deleted"); setDeleting(null); } })}>
              Delete window
            </button>
          </div>
        </Modal>
      )}
    </section>
  );
}
