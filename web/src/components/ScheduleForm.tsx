import { useState } from "react";
import { ApiError, errorMessage } from "../api/client";
import type { Schedule, ScheduleInput, Weekday } from "../api/types";
import {
  WEEKDAYS, WEEKDAY_LABEL, allTimezones, browserTimezone, isValidTimezone, isoToZonedLocal, zonedLocalToIso,
} from "../lib/time";

type Errors = Record<string, string>;

/** Validates the form and builds the API payload. Exported for tests. */
export function buildScheduleInput(f: {
  name: string; reason: string; kind: "one_off" | "recurring"; timezone: string; enabled: boolean;
  start: string; end: string; weekdays: Weekday[]; startTime: string; endTime: string;
  validFrom: string; validUntil: string;
}): { input?: ScheduleInput; errors: Errors } {
  const errors: Errors = {};
  if (!isValidTimezone(f.timezone)) errors.timezone = "Enter an IANA timezone such as Europe/Dublin";
  const base: ScheduleInput = {
    kind: f.kind, timezone: f.timezone, enabled: f.enabled,
    ...(f.name.trim() ? { name: f.name.trim() } : {}), ...(f.reason.trim() ? { reason: f.reason.trim() } : {}),
  };
  if (f.kind === "one_off") {
    if (!f.start) errors.starts_at = "Choose a start";
    if (!f.end) errors.ends_at = "Choose an end";
    if (!errors.timezone && f.start && f.end) {
      const s = zonedLocalToIso(f.start, f.timezone), e = zonedLocalToIso(f.end, f.timezone);
      if (Date.parse(s) >= Date.parse(e)) errors.ends_at = "The end must be after the start";
      else Object.assign(base, { starts_at: s, ends_at: e });
    }
  } else {
    if (!f.weekdays.length) errors.weekdays = "Pick at least one day";
    if (!f.startTime) errors.start_time = "Choose a start time";
    if (!f.endTime) errors.end_time = "Choose an end time";
    if (f.validFrom && f.validUntil && f.validFrom > f.validUntil) errors.valid_until = "Must not be before “valid from”";
    Object.assign(base, {
      weekdays: WEEKDAYS.filter((d) => f.weekdays.includes(d)), start_time: f.startTime, end_time: f.endTime,
      ...(f.validFrom ? { valid_from: f.validFrom } : {}), ...(f.validUntil ? { valid_until: f.validUntil } : {}),
    });
  }
  return Object.keys(errors).length ? { errors } : { input: base, errors };
}

function Field({ id, label, hint, error, children }: { id: string; label: string; hint?: string; error?: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint && <span className="hint">{hint}</span>}
      {error && <span className="error" id={`${id}-err`} role="alert">{error}</span>}
    </div>
  );
}

export function ScheduleForm({
  initial, pending, error, onSubmit, onCancel,
}: {
  initial?: Schedule; pending: boolean; error: unknown;
  onSubmit: (i: ScheduleInput) => void; onCancel: () => void;
}) {
  const tz0 = initial?.timezone ?? browserTimezone();
  const [name, setName] = useState(initial?.name ?? "");
  const [reason, setReason] = useState(initial?.reason ?? "");
  const [kind, setKind] = useState<"one_off" | "recurring">(initial?.kind ?? "one_off");
  const [timezone, setTimezone] = useState(tz0);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [start, setStart] = useState(initial?.starts_at && isValidTimezone(tz0) ? isoToZonedLocal(initial.starts_at, tz0) : "");
  const [end, setEnd] = useState(initial?.ends_at && isValidTimezone(tz0) ? isoToZonedLocal(initial.ends_at, tz0) : "");
  const [weekdays, setWeekdays] = useState<Weekday[]>(initial?.weekdays ?? ["fri"]);
  const [startTime, setStartTime] = useState(initial?.start_time ?? "17:00");
  const [endTime, setEndTime] = useState(initial?.end_time ?? "09:00");
  const [validFrom, setValidFrom] = useState(initial?.valid_from ?? "");
  const [validUntil, setValidUntil] = useState(initial?.valid_until ?? "");
  const [errors, setErrors] = useState<Errors>({});

  const serverFields = error instanceof ApiError ? error.fields ?? {} : {};
  const err = (k: string) => errors[k] ?? serverFields[k];

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const r = buildScheduleInput({ name, reason, kind, timezone, enabled, start, end, weekdays, startTime, endTime, validFrom, validUntil });
    setErrors(r.errors);
    if (r.input) onSubmit(r.input);
  };
  const toggle = (d: Weekday) => setWeekdays((w) => (w.includes(d) ? w.filter((x) => x !== d) : [...w, d]));

  const inv = (k: string) => ({ "aria-invalid": err(k) ? true : undefined, "aria-describedby": err(k) ? `${k}-err` : undefined });

  return (
    <form onSubmit={submit} noValidate>
      <Field id="name" error={err("name")} label="Name">
        <input id="name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)} placeholder="e.g. Release train" />
      </Field>
      <fieldset>
        <legend>Type</legend>
        <div className="seg" role="radiogroup" aria-label="Type">
          {(["one_off", "recurring"] as const).map((k) => (
            <button key={k} type="button" role="radio" aria-checked={kind === k} aria-pressed={kind === k} onClick={() => setKind(k)}>
              {k === "one_off" ? "One-off" : "Recurring"}
            </button>
          ))}
        </div>
      </fieldset>
      <Field id="timezone" error={err("timezone")} label="Timezone" hint="Times below are in this timezone; recurring windows follow its DST.">
        <input id="timezone" list="tz-list" value={timezone} onChange={(e) => setTimezone(e.target.value)} {...inv("timezone")} />
        <datalist id="tz-list">{allTimezones().map((z) => <option key={z} value={z} />)}</datalist>
      </Field>
      {kind === "one_off" ? (
        <div className="row" style={{ alignItems: "flex-start" }}>
          <Field id="starts_at" error={err("starts_at")} label="Starts">
            <input id="starts_at" type="datetime-local" value={start} onChange={(e) => setStart(e.target.value)} {...inv("starts_at")} />
          </Field>
          <Field id="ends_at" error={err("ends_at")} label="Ends (thaws automatically)">
            <input id="ends_at" type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} {...inv("ends_at")} />
          </Field>
        </div>
      ) : (
        <>
          <fieldset className="field">
            <legend>Days</legend>
            <div className="row daypick" role="group" aria-label="Days">
              {WEEKDAYS.map((d) => (
                <label key={d}>
                  <input type="checkbox" className="sr-only" checked={weekdays.includes(d)} onChange={() => toggle(d)} />
                  {WEEKDAY_LABEL[d]}
                </label>
              ))}
            </div>
            {err("weekdays") && <span className="error" role="alert">{err("weekdays")}</span>}
          </fieldset>
          <div className="row" style={{ alignItems: "flex-start" }}>
            <Field id="start_time" error={err("start_time")} label="From">
              <input id="start_time" type="time" value={startTime} onChange={(e) => setStartTime(e.target.value)} {...inv("start_time")} />
            </Field>
            <Field id="end_time" error={err("end_time")} label="Until" hint={endTime <= startTime ? "Ends the next day" : undefined}>
              <input id="end_time" type="time" value={endTime} onChange={(e) => setEndTime(e.target.value)} {...inv("end_time")} />
            </Field>
          </div>
          <div className="row" style={{ alignItems: "flex-start" }}>
            <Field id="valid_from" error={err("valid_from")} label="Valid from (optional)">
              <input id="valid_from" type="date" value={validFrom} onChange={(e) => setValidFrom(e.target.value)} />
            </Field>
            <Field id="valid_until" error={err("valid_until")} label="Valid until (optional)">
              <input id="valid_until" type="date" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} {...inv("valid_until")} />
            </Field>
          </div>
        </>
      )}
      <Field id="reason" error={err("reason")} label="Reason (optional)">
        <input id="reason" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <label className="row" style={{ marginBottom: ".5rem" }}>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
      </label>
      {error != null && <p className="error" role="alert">{errorMessage(error)}</p>}
      <div className="modal-actions">
        <button type="button" className="btn" onClick={onCancel}>Cancel</button>
        <button type="submit" className="btn primary" disabled={pending}>{pending ? "Saving…" : initial ? "Save window" : "Create window"}</button>
      </div>
    </form>
  );
}
