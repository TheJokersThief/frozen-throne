import { http, HttpResponse, type HttpHandler } from "msw";
import type { ApiErrorBody, ScheduleInput } from "../api/types";
import { MockDb, type MockRepo, type MockSchedule } from "./db";

const BASE = "*/api/v1";

function err(status: number, code: ApiErrorBody["code"], message: string, fields?: Record<string, string>) {
  return HttpResponse.json({ code, message, ...(fields ? { fields } : {}) } satisfies ApiErrorBody, { status });
}
const notFound = () => err(404, "not_found", "not found");
const forbidden = (what: string) => err(403, "forbidden", `insufficient permissions on ${what}`);
const badRequest = (message: string, fields?: Record<string, string>) => err(400, "bad_request", message, fields);

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
type Shape = Record<string, "string" | "boolean" | "string[]" | "datetime">;

/**
 * Strict JSON body decoding like the Go API (DisallowUnknownFields): unknown
 * keys and wrongly typed values are a 400. An empty body is a 400 unless
 * `optional`, in which case it decodes to {}.
 */
async function readBody<T>(request: Request, shape: Shape, optional = false): Promise<T | Response> {
  const text = await request.text();
  if (!text.trim()) return optional ? ({} as T) : badRequest("request body is required");
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch (e) {
    return badRequest(`invalid JSON: ${(e as Error).message}`);
  }
  if (v === null || typeof v !== "object" || Array.isArray(v)) return badRequest("invalid JSON: expected an object");
  for (const [k, val] of Object.entries(v)) {
    const t = shape[k];
    if (!t) return badRequest(`invalid JSON: unknown field "${k}"`);
    if (val === null) continue;
    const ok =
      t === "string" ? typeof val === "string"
      : t === "boolean" ? typeof val === "boolean"
      : t === "datetime" ? typeof val === "string" && RFC3339.test(val) && !Number.isNaN(Date.parse(val))
      : Array.isArray(val) && val.every((x) => typeof x === "string");
    if (!ok) return badRequest(`invalid JSON: field "${k}" has the wrong type`);
  }
  return v as T;
}

const REASON_SHAPE: Shape = { reason: "string" };

/** limit (1..200, default 50) and offset (>= 0), 400 with per-field messages otherwise. */
function paging(s: URLSearchParams): { limit: number; offset: number } | Response {
  let limit = 50;
  let offset = 0;
  const fields: Record<string, string> = {};
  const l = s.get("limit");
  if (l) {
    if (/^[+-]?\d+$/.test(l) && +l >= 1 && +l <= 200) limit = +l;
    else fields.limit = "must be an integer between 1 and 200";
  }
  const o = s.get("offset");
  if (o) {
    if (/^[+-]?\d+$/.test(o) && +o >= 0) offset = +o;
    else fields.offset = "must be a non-negative integer";
  }
  return Object.keys(fields).length ? badRequest("invalid paging parameters", fields) : { limit, offset };
}

/** Mirrors Go's mustReason: up to 500 characters. */
const reasonTooLong = (reason: string | undefined) =>
  reason && reason.length > 500 ? badRequest("reason is too long", { reason: "must be at most 500 characters" }) : undefined;

/**
 * MSW handlers implementing api/openapi.yaml against an in-memory MockDb.
 * Used by `npm run dev:mock` (browser) and by the component tests (node).
 * web/src/mocks/contract.test.ts proves they stay in line with the spec.
 */
export function createHandlers(db: MockDb, opts: { latencyMs?: number } = {}): HttpHandler[] {
  const delay = () => (opts.latencyMs ? new Promise((r) => setTimeout(r, opts.latencyMs)) : undefined);

  // Wrap a resolver. Same order as the Go dispatcher: CSRF header and
  // Content-Type on mutations, then authentication, then the handler.
  type Ctx = { params: Record<string, string | readonly string[] | undefined>; request: Request };
  const route = (
    method: "get" | "post" | "put" | "delete",
    path: string,
    fn: (c: Ctx) => Response | Promise<Response>,
    { auth = true } = {},
  ) =>
    http[method](`${BASE}${path}`, async (c) => {
      await delay();
      if (method !== "get") {
        if (c.request.headers.get("X-Requested-With") !== "frozen-throne") {
          return err(403, "forbidden", "missing X-Requested-With header");
        }
        const ct = (c.request.headers.get("Content-Type") ?? "").split(";")[0].trim().toLowerCase();
        if (ct !== "application/json" && (await c.request.clone().text()) !== "") {
          return badRequest("Content-Type must be application/json");
        }
      }
      if (auth && !db.loggedIn) return err(401, "unauthorized", "login required");
      db.sweepBypasses();
      return fn(c as Ctx);
    });

  const p = (c: Ctx, k: string) => String(c.params[k]);
  const repoOf = (c: Ctx): MockRepo | undefined => db.findRepo(p(c, "owner"), p(c, "repo"));
  const q = (c: Ctx) => new URL(c.request.url).searchParams;
  /** Positive integer path parameter, else a 400 like Go's pathInt. */
  const pathInt = (c: Ctx, name: string): number | Response => {
    const raw = p(c, name);
    return /^\d+$/.test(raw) && +raw >= 1 ? +raw : badRequest(`invalid ${name}`, { [name]: "must be a positive integer" });
  };
  const repoName = (r: MockRepo) => `${r.owner}/${r.name}`;
  const can = (r: MockRepo, k: "can_freeze" | "can_thaw" | "can_bypass" | "can_manage_schedules") =>
    !!db.summary(r).permissions?.[k];

  const SCHEDULE_SHAPE: Shape = {
    name: "string", reason: "string", kind: "string", timezone: "string", enabled: "boolean",
    starts_at: "datetime", ends_at: "datetime", weekdays: "string[]", start_time: "string", end_time: "string",
    valid_from: "string", valid_until: "string",
  };

  /** Visibility and permission of the scope a stored schedule belongs to (Go's loadSchedule). */
  const scheduleAccess = (s: MockSchedule, write: boolean): Response | undefined => {
    if (s.scope === "org") {
      const o = db.findOrg(s.org);
      if (!o) return notFound();
      return write && !db.orgOut(o).permissions?.can_manage_schedules ? forbidden(o.login) : undefined;
    }
    const r = db.findRepo(...(s.repo!.split("/") as [string, string]));
    if (!r) return notFound();
    return write && !can(r, "can_manage_schedules") ? forbidden(repoName(r)) : undefined;
  };

  /** Create a schedule from a validated request body in the given scope. */
  const createSchedule = async (c: Ctx, scope: "org" | "repo", org: string, repo?: string) => {
    const input = await readBody<ScheduleInput>(c.request, SCHEDULE_SHAPE);
    if (input instanceof Response) return input;
    const fields = db.validateSchedule(input);
    if (Object.keys(fields).length) return badRequest("invalid schedule", fields);
    const s = db.buildSchedule(input, scope, org, repo);
    db.schedules.push(s);
    db.log("schedule.create", org, { repo, reason: s.reason, details: { schedule_id: s.id, name: s.name, kind: s.kind } });
    return HttpResponse.json(db.schedule(s), { status: 201 });
  };

  return [
    route("get", "/healthz", () => HttpResponse.json({ status: "ok" }), { auth: false }),

    // ---- auth ----
    route("get", "/auth/me", () => HttpResponse.json(db.user)),
    route("post", "/auth/logout", () => {
      db.loggedIn = false;
      return new HttpResponse(null, { status: 204 });
    }, { auth: false }),
    // Mock-only: stands in for the GitHub OAuth redirect round-trip.
    route("post", "/auth/mock-login", () => {
      db.loggedIn = true;
      return new HttpResponse(null, { status: 204 });
    }, { auth: false }),

    // ---- repos ----
    route("get", "/repos", (c) => {
      const s = q(c);
      const page = paging(s);
      if (page instanceof Response) return page;
      const state = s.get("state");
      if (state && state !== "frozen" && state !== "thawed") {
        return badRequest("invalid state", { state: "must be frozen or thawed" });
      }
      const text = (s.get("q") ?? "").toLowerCase();
      const org = (s.get("org") ?? "").toLowerCase();
      let list = db.visibleRepos().map((r) => db.summary(r));
      if (text) list = list.filter((r) => r.full_name.toLowerCase().includes(text));
      if (org) list = list.filter((r) => r.owner.toLowerCase() === org);
      if (state) list = list.filter((r) => r.state.frozen === (state === "frozen"));
      // The Go API orders by lower-cased full name only; the UI surfaces frozen repos itself.
      list.sort((a, b) => (a.full_name.toLowerCase() < b.full_name.toLowerCase() ? -1 : 1));
      return HttpResponse.json({ items: list.slice(page.offset, page.offset + page.limit), total: list.length });
    }),
    route("get", "/repos/:owner/:repo", (c) => {
      const r = repoOf(c);
      return r ? HttpResponse.json(db.detail(r)) : notFound();
    }),
    route("post", "/repos/:owner/:repo/freeze", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_freeze")) return forbidden(repoName(r));
      const b = await readBody<{ reason?: string }>(c.request, REASON_SHAPE, true);
      if (b instanceof Response) return b;
      const tooLong = reasonTooLong(b.reason);
      if (tooLong) return tooLong;
      if (r.freeze) return err(409, "conflict", "repo is already frozen");
      const reason = b.reason?.trim() || undefined;
      r.freeze = { id: db.nextId(), reason, by: db.user.login, since: db.iso() };
      db.log("freeze", r.owner, { repo: repoName(r), scope: "repo", reason });
      return HttpResponse.json({ state: db.repoState(r), rechecking: r.open_prs }, { status: 201 });
    }),
    route("delete", "/repos/:owner/:repo/freeze", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_thaw")) return forbidden(repoName(r));
      if (!r.freeze) return notFound();
      r.freeze = undefined;
      db.log("thaw", r.owner, { repo: repoName(r), scope: "repo" });
      db.sweepBypasses();
      return HttpResponse.json({ state: db.repoState(r), rechecking: r.open_prs });
    }),
    route("put", "/repos/:owner/:repo/public-status", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_freeze")) return forbidden(repoName(r));
      const b = await readBody<{ enabled?: boolean }>(c.request, { enabled: "boolean" });
      if (b instanceof Response) return b;
      if (typeof b.enabled !== "boolean") return badRequest("enabled is required", { enabled: "required" });
      r.public_status = b.enabled;
      db.log("public_status.update", r.owner, { repo: repoName(r), details: { enabled: b.enabled } });
      return HttpResponse.json({ enabled: b.enabled });
    }),

    // ---- orgs ----
    route("get", "/orgs", () => HttpResponse.json({ items: db.orgs.map((o) => db.orgOut(o)) })),
    route("get", "/orgs/:org", (c) => {
      const o = db.findOrg(p(c, "org"));
      return o ? HttpResponse.json(db.orgOut(o)) : notFound();
    }),
    route("post", "/orgs/:org/freeze", async (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      if (!db.orgOut(o).permissions?.can_freeze) return forbidden(o.login);
      const b = await readBody<{ reason?: string }>(c.request, REASON_SHAPE, true);
      if (b instanceof Response) return b;
      const tooLong = reasonTooLong(b.reason);
      if (tooLong) return tooLong;
      if (o.freeze) return err(409, "conflict", "org is already frozen");
      const reason = b.reason?.trim() || undefined;
      o.freeze = { id: db.nextId(), reason, by: db.user.login, since: db.iso() };
      db.log("freeze", o.login, { scope: "org", reason });
      const n = db.repos.filter((r) => r.owner === o.login).reduce((a, r) => a + r.open_prs, 0);
      return HttpResponse.json({ org: db.orgOut(o), rechecking: n }, { status: 201 });
    }),
    route("delete", "/orgs/:org/freeze", (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      if (!db.orgOut(o).permissions?.can_thaw) return forbidden(o.login);
      if (!o.freeze) return notFound();
      o.freeze = undefined;
      db.log("thaw", o.login, { scope: "org" });
      db.sweepBypasses();
      const n = db.repos.filter((r) => r.owner === o.login).reduce((a, r) => a + r.open_prs, 0);
      return HttpResponse.json({ org: db.orgOut(o), rechecking: n });
    }),

    // ---- schedules ----
    route("get", "/orgs/:org/schedules", (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      const items = db.schedules.filter((s) => s.scope === "org" && s.org === o.login).map((s) => db.schedule(s));
      return HttpResponse.json({ items });
    }),
    route("post", "/orgs/:org/schedules", (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      if (!db.orgOut(o).permissions?.can_manage_schedules) return forbidden(o.login);
      return createSchedule(c, "org", o.login);
    }),
    route("get", "/repos/:owner/:repo/schedules", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      const full = repoName(r);
      return HttpResponse.json({
        items: db.schedules.filter((s) => s.scope === "repo" && s.repo === full).map((s) => db.schedule(s)),
      });
    }),
    route("post", "/repos/:owner/:repo/schedules", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_manage_schedules")) return forbidden(repoName(r));
      return createSchedule(c, "repo", r.owner, repoName(r));
    }),
    route("get", "/schedules/:id", (c) => {
      const s = db.schedules.find((x) => x.id === Number(p(c, "id")));
      if (!s) return notFound();
      return scheduleAccess(s, false) ?? HttpResponse.json(db.schedule(s));
    }),
    route("put", "/schedules/:id", async (c) => {
      const i = db.schedules.findIndex((x) => x.id === Number(p(c, "id")));
      if (i < 0) return notFound();
      const prev = db.schedules[i];
      const denied = scheduleAccess(prev, true);
      if (denied) return denied;
      const input = await readBody<ScheduleInput>(c.request, SCHEDULE_SHAPE);
      if (input instanceof Response) return input;
      const fields = db.validateSchedule(input);
      if (Object.keys(fields).length) return badRequest("invalid schedule", fields);
      db.schedules[i] = db.buildSchedule(input, prev.scope, prev.org, prev.repo, prev);
      db.log("schedule.update", prev.org, {
        repo: prev.repo, reason: db.schedules[i].reason,
        details: { schedule_id: prev.id, name: db.schedules[i].name, kind: db.schedules[i].kind },
      });
      db.sweepBypasses();
      return HttpResponse.json(db.schedule(db.schedules[i]));
    }),
    route("delete", "/schedules/:id", (c) => {
      const i = db.schedules.findIndex((x) => x.id === Number(p(c, "id")));
      if (i < 0) return notFound();
      const prev = db.schedules[i];
      const denied = scheduleAccess(prev, true);
      if (denied) return denied;
      db.schedules.splice(i, 1);
      db.log("schedule.delete", prev.org, {
        repo: prev.repo, reason: prev.reason, details: { schedule_id: prev.id, name: prev.name, kind: prev.kind },
      });
      db.sweepBypasses();
      return new HttpResponse(null, { status: 204 });
    }),

    // ---- bypass ----
    route("get", "/repos/:owner/:repo/bypasses", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      const active = q(c).get("active");
      if (active && active !== "true" && active !== "false") {
        return badRequest("invalid active", { active: "must be true or false" });
      }
      const page = paging(q(c));
      if (page instanceof Response) return page;
      const full = repoName(r);
      const items = db.bypasses
        .filter((b) => b.repo === full && (active === "false" || !b.ended_at))
        .reverse()
        .slice(0, page.limit);
      return HttpResponse.json({ items });
    }),
    route("post", "/repos/:owner/:repo/pulls/:number/bypass", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_bypass")) return forbidden(repoName(r));
      const number = pathInt(c, "number");
      if (number instanceof Response) return number;
      const b = await readBody<{ reason?: string }>(c.request, REASON_SHAPE);
      if (b instanceof Response) return b;
      const reason = (b.reason ?? "").trim();
      if (!reason) return badRequest("a reason is required", { reason: "required" });
      if (reason.length > 500) return badRequest("reason is too long", { reason: "must be at most 500 characters" });
      const full = repoName(r);
      if (db.bypasses.some((x) => x.repo === full && x.pr_number === number && !x.ended_at)) {
        return err(409, "conflict", "this pull request already has an active bypass");
      }
      const created = { id: db.nextId(), repo: full, pr_number: number, reason, created_by: db.user.login, created_at: db.iso() };
      db.bypasses.push(created);
      db.log("bypass.create", r.owner, { repo: full, pr_number: number, reason });
      return HttpResponse.json(created, { status: 201 });
    }),
    route("delete", "/repos/:owner/:repo/pulls/:number/bypass", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!can(r, "can_bypass")) return forbidden(repoName(r));
      const number = pathInt(c, "number");
      if (number instanceof Response) return number;
      const b = db.bypasses.find((x) => x.repo === repoName(r) && x.pr_number === number && !x.ended_at);
      if (!b) return notFound();
      b.ended_at = db.iso();
      b.end_reason = "revoked";
      db.log("bypass.end", r.owner, { repo: b.repo, pr_number: number, details: { end_reason: "revoked" } });
      return new HttpResponse(null, { status: 204 });
    }),

    // ---- audit ----
    route("get", "/audit", (c) => {
      const s = q(c);
      const page = paging(s);
      if (page instanceof Response) return page;
      const action = s.get("action");
      if (action && !AUDIT_ACTIONS.has(action)) return badRequest("invalid action", { action: "unknown action" });
      const before = s.get("before");
      if (before && !(/^\d+$/.test(before) && +before >= 1)) {
        return badRequest("invalid before", { before: "must be a positive integer" });
      }
      const org = s.get("org");
      if (org && !db.findOrg(org)) return notFound();
      const repo = s.get("repo");
      if (repo) {
        const [owner, name] = repo.split("/");
        if (!repo.includes("/")) return badRequest("invalid repo", { repo: "must be owner/name" });
        if (!db.findRepo(owner, name)) return notFound();
      }
      const orgs = new Set(db.orgs.map((o) => o.login));
      let list = db.audit.filter((e) => {
        if (!orgs.has(e.org)) return false;
        if (!e.repo) return true;
        // Entries of repos the caller cannot see are hidden (unknown/removed repos stay listed).
        const [owner, name] = e.repo.split("/");
        const known = db.repos.find((r) => r.owner === owner && r.name === name);
        return !known || db.repoRole(known) !== "none";
      });
      for (const k of ["org", "repo", "actor", "action"] as const) {
        if (s.get(k)) list = list.filter((e) => e[k] === s.get(k));
      }
      if (before) list = list.filter((e) => e.id < Number(before));
      const items = list.slice(0, page.limit);
      return HttpResponse.json({
        items,
        ...(list.length > page.limit ? { next_before: items[items.length - 1].id } : {}),
      });
    }),

    // ---- public (no session; only opted-in repos, otherwise an identical 404) ----
    route("get", "/public/repos/:owner/:repo/status", (c) => {
      const r = db.repos.find((x) => x.owner.toLowerCase() === p(c, "owner").toLowerCase() && x.name.toLowerCase() === p(c, "repo").toLowerCase());
      if (!r?.public_status) return notFound();
      return HttpResponse.json(db.publicStatus(r));
    }, { auth: false }),
    route("get", "/public/repos/:owner/:repo/badge.svg", (c) => {
      const r = db.repos.find((x) => x.owner.toLowerCase() === p(c, "owner").toLowerCase() && x.name.toLowerCase() === p(c, "repo").toLowerCase());
      const known = r?.public_status ? db.publicStatus(r) : undefined;
      const [value, color, status] = !known ? ["unknown", "#9f9f9f", 404] as const
        : known.frozen ? ["frozen", "#e05d44", 200] as const : ["open", "#4c1", 200] as const;
      return new HttpResponse(badgeSvg("merge freeze", value, color), {
        status, headers: { "Content-Type": "image/svg+xml", "Cache-Control": "public, max-age=60" },
      });
    }, { auth: false }),
  ];
}

const AUDIT_ACTIONS = new Set<string>([
  "freeze", "thaw", "schedule.create", "schedule.update", "schedule.delete",
  "bypass.create", "bypass.end", "slack.update", "public_status.update",
]);

function badgeSvg(label: string, value: string, color: string) {
  const lw = 7 * label.length + 10;
  const vw = 7 * value.length + 10;
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${lw + vw}" height="20" role="img" aria-label="${label}: ${value}">` +
    `<g><rect width="${lw}" height="20" fill="#555"/><rect x="${lw}" width="${vw}" height="20" fill="${color}"/></g>` +
    `<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">` +
    `<text x="${lw / 2}" y="14">${label}</text><text x="${lw + vw / 2}" y="14">${value}</text></g></svg>`
  );
}
