import type { Weekday } from "../api/types";

export const WEEKDAYS: Weekday[] = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
export const WEEKDAY_LABEL: Record<Weekday, string> = {
  mon: "Mon", tue: "Tue", wed: "Wed", thu: "Thu", fri: "Fri", sat: "Sat", sun: "Sun",
};
const JS_DAY: Weekday[] = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

export function isValidTimezone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat("en", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

export function browserTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function allTimezones(): string[] {
  const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
  const list = f ? f("timeZone") : [];
  return list.includes("UTC") ? list : ["UTC", ...list];
}

interface Parts { y: number; mo: number; d: number; h: number; mi: number; wd: Weekday }

/** Wall-clock parts of an instant in a timezone. */
export function zonedParts(ms: number, tz: string): Parts {
  const p = Object.fromEntries(
    new Intl.DateTimeFormat("en-US", {
      timeZone: tz, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric",
      hour: "numeric", minute: "numeric", weekday: "short",
    }).formatToParts(new Date(ms)).map((x) => [x.type, x.value]),
  );
  return {
    y: +p.year, mo: +p.month, d: +p.day, h: +p.hour, mi: +p.minute,
    wd: p.weekday.slice(0, 3).toLowerCase() as Weekday,
  };
}

const pad = (n: number) => String(n).padStart(2, "0");

/** Instant -> `YYYY-MM-DDTHH:mm` wall clock in tz (for datetime-local inputs). */
export function isoToZonedLocal(iso: string, tz: string): string {
  const p = zonedParts(Date.parse(iso), tz);
  return `${p.y}-${pad(p.mo)}-${pad(p.d)}T${pad(p.h)}:${pad(p.mi)}`;
}

/** `YYYY-MM-DDTHH:mm` wall clock in tz -> RFC 3339 UTC instant. */
export function zonedLocalToIso(local: string, tz: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local);
  if (!m) throw new Error("invalid local datetime");
  const [y, mo, d, h, mi] = m.slice(1).map(Number);
  const asUtc = Date.UTC(y, mo - 1, d, h, mi);
  // Two refinement passes handle offsets and DST transitions.
  let guess = asUtc;
  for (let i = 0; i < 2; i++) {
    const p = zonedParts(guess, tz);
    guess += asUtc - Date.UTC(p.y, p.mo - 1, p.d, p.h, p.mi);
  }
  return new Date(guess).toISOString().replace(".000Z", "Z");
}

/** Is a recurring window active at `ms`? Used by the mock API only. */
export function recurringActive(
  s: { weekdays?: Weekday[]; start_time?: string; end_time?: string; timezone: string },
  ms: number,
): boolean {
  if (!s.weekdays || !s.start_time || !s.end_time) return false;
  const p = zonedParts(ms, s.timezone);
  const now = p.h * 60 + p.mi;
  const toMin = (t: string) => +t.slice(0, 2) * 60 + +t.slice(3);
  const start = toMin(s.start_time), end = toMin(s.end_time);
  const today = s.weekdays.includes(p.wd);
  if (end > start) return today && now >= start && now < end;
  if (today && now >= start) return true; // overnight window, first evening
  const yesterday = JS_DAY[(JS_DAY.indexOf(p.wd) + 6) % 7];
  return s.weekdays.includes(yesterday) && now < end;
}

export function formatDateTime(iso: string, tz?: string): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium", timeStyle: "short", timeZone: tz,
  }).format(new Date(iso));
}

export function formatRelative(iso: string, now = Date.now()): string {
  const diff = Date.parse(iso) - now;
  const abs = Math.abs(diff);
  const units: [number, Intl.RelativeTimeFormatUnit][] = [
    [86_400_000, "day"], [3_600_000, "hour"], [60_000, "minute"],
  ];
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [ms, unit] of units) {
    if (abs >= ms) return rtf.format(Math.round(diff / ms), unit);
  }
  return rtf.format(0, "minute");
}
