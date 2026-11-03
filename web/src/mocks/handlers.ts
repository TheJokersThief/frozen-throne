import { http, HttpResponse, type HttpHandler } from "msw";
import type { ApiErrorBody, ScheduleInput } from "../api/types";
import { MockDb, type MockRepo } from "./db";

const BASE = "*/api/v1";

function err(status: number, code: ApiErrorBody["code"], message: string, fields?: Record<string, string>) {
  return HttpResponse.json({ code, message, ...(fields ? { fields } : {}) } satisfies ApiErrorBody, { status });
}
const notFound = () => err(404, "not_found", "Not found");
const forbidden = () => err(403, "forbidden", "You do not have permission to do this");

/**
 * MSW handlers implementing api/openapi.yaml against an in-memory MockDb.
 * Used by `npm run dev:mock` (browser) and by the component tests (node).
 */
export function createHandlers(db: MockDb, opts: { latencyMs?: number } = {}): HttpHandler[] {
  const delay = () => (opts.latencyMs ? new Promise((r) => setTimeout(r, opts.latencyMs)) : undefined);

  // Wrap a resolver: latency, auth, CSRF header on mutations, lazy bypass sweep.
  type Ctx = { params: Record<string, string | readonly string[] | undefined>; request: Request };
  const route = (
    method: "get" | "post" | "put" | "delete",
    path: string,
    fn: (c: Ctx) => Response | Promise<Response>,
    { auth = true } = {},
  ) =>
    http[method](`${BASE}${path}`, async (c) => {
      await delay();
      if (auth && !db.loggedIn) return err(401, "unauthorized", "Not logged in");
      if (method !== "get" && c.request.headers.get("X-Requested-With") !== "frozen-throne") {
        return err(403, "forbidden", "Missing X-Requested-With header");
      }
      db.sweepBypasses();
      return fn(c as Ctx);
    });

  const p = (c: Ctx, k: string) => String(c.params[k]);
  const repoOf = (c: Ctx): MockRepo | undefined => db.findRepo(p(c, "owner"), p(c, "repo"));
  const body = async <T,>(c: Ctx) => (await c.request.json().catch(() => ({}))) as T;
  const q = (c: Ctx) => new URL(c.request.url).searchParams;

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
      const text = (s.get("q") ?? "").toLowerCase();
      let list = db.visibleRepos().map((r) => db.summary(r));
      if (text) list = list.filter((r) => r.full_name.toLowerCase().includes(text));
      if (s.get("org")) list = list.filter((r) => r.owner === s.get("org"));
      if (s.get("state")) list = list.filter((r) => r.state.frozen === (s.get("state") === "frozen"));
      list.sort((a, b) => Number(b.state.frozen) - Number(a.state.frozen) || a.full_name.localeCompare(b.full_name));
      const limit = Number(s.get("limit") ?? 50);
      const offset = Number(s.get("offset") ?? 0);
      return HttpResponse.json({ items: list.slice(offset, offset + limit), total: list.length });
    }),
    route("get", "/repos/:owner/:repo", (c) => {
      const r = repoOf(c);
      return r ? HttpResponse.json(db.detail(r)) : notFound();
    }),
    route("post", "/repos/:owner/:repo/freeze", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!db.summary(r).permissions?.can_freeze) return forbidden();
      if (r.freeze) return err(409, "conflict", "Repo is already frozen");
      const { reason } = await body<{ reason?: string }>(c);
      r.freeze = { id: db.nextId(), reason, by: db.user.login, since: db.iso() };
      db.log("freeze", r.owner, { repo: `${r.owner}/${r.name}`, reason });
      return HttpResponse.json({ state: db.repoState(r), rechecking: r.open_prs }, { status: 201 });
    }),
    route("delete", "/repos/:owner/:repo/freeze", (c) => {
      const r = repoOf(c);
      if (!r || !r.freeze) return notFound();
      if (!db.summary(r).permissions?.can_thaw) return forbidden();
      r.freeze = undefined;
      db.log("thaw", r.owner, { repo: `${r.owner}/${r.name}` });
      db.sweepBypasses();
      return HttpResponse.json({ state: db.repoState(r), rechecking: r.open_prs });
    }),
    route("put", "/repos/:owner/:repo/public-status", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!db.summary(r).permissions?.can_freeze) return forbidden();
      const { enabled } = await body<{ enabled: boolean }>(c);
      r.public_status = enabled;
      db.log("public_status.update", r.owner, { repo: `${r.owner}/${r.name}`, details: { enabled } });
      return HttpResponse.json({ enabled });
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
      if (!db.orgOut(o).permissions?.can_freeze) return forbidden();
      if (o.freeze) return err(409, "conflict", "Org is already frozen");
      const { reason } = await body<{ reason?: string }>(c);
      o.freeze = { id: db.nextId(), reason, by: db.user.login, since: db.iso() };
      db.log("freeze", o.login, { reason });
      const n = db.repos.filter((r) => r.owner === o.login).reduce((a, r) => a + r.open_prs, 0);
      return HttpResponse.json({ org: db.orgOut(o), rechecking: n }, { status: 201 });
    }),
    route("delete", "/orgs/:org/freeze", (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o || !o.freeze) return notFound();
      if (!db.orgOut(o).permissions?.can_thaw) return forbidden();
      o.freeze = undefined;
      db.log("thaw", o.login);
      db.sweepBypasses();
      return HttpResponse.json({ org: db.orgOut(o) });
    }),

    // ---- schedules ----
    route("get", "/orgs/:org/schedules", (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      const items = db.schedules.filter((s) => s.scope === "org" && s.org === o.login).map((s) => db.schedule(s));
      return HttpResponse.json({ items });
    }),
    route("post", "/orgs/:org/schedules", async (c) => {
      const o = db.findOrg(p(c, "org"));
      if (!o) return notFound();
      if (!db.orgOut(o).permissions?.can_manage_schedules) return forbidden();
      const input = await body<ScheduleInput>(c);
      const fields = db.validateSchedule(input);
      if (Object.keys(fields).length) return err(400, "bad_request", "Invalid schedule", fields);
      const s = db.buildSchedule(input, "org", o.login);
      db.schedules.push(s);
      db.log("schedule.create", o.login, { details: { name: s.name, id: s.id } });
      return HttpResponse.json(db.schedule(s), { status: 201 });
    }),
    route("get", "/repos/:owner/:repo/schedules", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      const full = `${r.owner}/${r.name}`;
      return HttpResponse.json({
        items: db.schedules.filter((s) => s.scope === "repo" && s.repo === full).map((s) => db.schedule(s)),
      });
    }),
    route("post", "/repos/:owner/:repo/schedules", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!db.summary(r).permissions?.can_manage_schedules) return forbidden();
      const input = await body<ScheduleInput>(c);
      const fields = db.validateSchedule(input);
      if (Object.keys(fields).length) return err(400, "bad_request", "Invalid schedule", fields);
      const s = db.buildSchedule(input, "repo", r.owner, `${r.owner}/${r.name}`);
      db.schedules.push(s);
      db.log("schedule.create", r.owner, { repo: s.repo, details: { name: s.name, id: s.id } });
      return HttpResponse.json(db.schedule(s), { status: 201 });
    }),
    route("get", "/schedules/:id", (c) => {
      const s = db.schedules.find((x) => x.id === Number(p(c, "id")));
      return s ? HttpResponse.json(db.schedule(s)) : notFound();
    }),
    route("put", "/schedules/:id", async (c) => {
      const i = db.schedules.findIndex((x) => x.id === Number(p(c, "id")));
      if (i < 0) return notFound();
      const prev = db.schedules[i];
      const allowed =
        prev.scope === "org"
          ? db.orgOut(db.findOrg(prev.org)!).permissions?.can_manage_schedules
          : db.summary(db.repos.find((r) => `${r.owner}/${r.name}` === prev.repo)!).permissions?.can_manage_schedules;
      if (!allowed) return forbidden();
      const input = await body<ScheduleInput>(c);
      const fields = db.validateSchedule(input);
      if (Object.keys(fields).length) return err(400, "bad_request", "Invalid schedule", fields);
      db.schedules[i] = db.buildSchedule(input, prev.scope, prev.org, prev.repo, prev);
      db.log("schedule.update", prev.org, { repo: prev.repo, details: { name: input.name, id: prev.id } });
      db.sweepBypasses();
      return HttpResponse.json(db.schedule(db.schedules[i]));
    }),
    route("delete", "/schedules/:id", (c) => {
      const i = db.schedules.findIndex((x) => x.id === Number(p(c, "id")));
      if (i < 0) return notFound();
      const prev = db.schedules[i];
      const allowed =
        prev.scope === "org"
          ? db.orgOut(db.findOrg(prev.org)!).permissions?.can_manage_schedules
          : db.summary(db.repos.find((r) => `${r.owner}/${r.name}` === prev.repo)!).permissions?.can_manage_schedules;
      if (!allowed) return forbidden();
      db.schedules.splice(i, 1);
      db.log("schedule.delete", prev.org, { repo: prev.repo, details: { name: prev.name, id: prev.id } });
      db.sweepBypasses();
      return new HttpResponse(null, { status: 204 });
    }),

    // ---- bypass ----
    route("get", "/repos/:owner/:repo/bypasses", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      const onlyActive = q(c).get("active") !== "false";
      const full = `${r.owner}/${r.name}`;
      return HttpResponse.json({
        items: db.bypasses.filter((b) => b.repo === full && (!onlyActive || !b.ended_at)).reverse(),
      });
    }),
    route("post", "/repos/:owner/:repo/pulls/:number/bypass", async (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!db.summary(r).permissions?.can_bypass) return forbidden();
      const { reason } = await body<{ reason?: string }>(c);
      if (!reason?.trim()) return err(400, "bad_request", "A reason is required", { reason: "required" });
      const number = Number(p(c, "number"));
      const full = `${r.owner}/${r.name}`;
      if (db.bypasses.some((b) => b.repo === full && b.pr_number === number && !b.ended_at)) {
        return err(409, "conflict", `PR #${number} already has an active bypass`);
      }
      const b = { id: db.nextId(), repo: full, pr_number: number, reason: reason.trim(), created_by: db.user.login, created_at: db.iso() };
      db.bypasses.push(b);
      db.log("bypass.create", r.owner, { repo: full, pr_number: number, reason: b.reason });
      return HttpResponse.json(b, { status: 201 });
    }),
    route("delete", "/repos/:owner/:repo/pulls/:number/bypass", (c) => {
      const r = repoOf(c);
      if (!r) return notFound();
      if (!db.summary(r).permissions?.can_bypass) return forbidden();
      const number = Number(p(c, "number"));
      const b = db.bypasses.find((x) => x.repo === `${r.owner}/${r.name}` && x.pr_number === number && !x.ended_at);
      if (!b) return notFound();
      b.ended_at = db.iso();
      b.end_reason = "revoked";
      db.log("bypass.end", r.owner, { repo: b.repo, pr_number: number, details: { end_reason: "revoked" } });
      return new HttpResponse(null, { status: 204 });
    }),

    // ---- audit ----
    route("get", "/audit", (c) => {
      const s = q(c);
      const visible = new Set(db.orgs.map((o) => o.login));
      let list = db.audit.filter((e) => visible.has(e.org));
      for (const k of ["org", "repo", "actor", "action"] as const) {
        if (s.get(k)) list = list.filter((e) => e[k] === s.get(k));
      }
      if (s.get("before")) list = list.filter((e) => e.id < Number(s.get("before")));
      const limit = Number(s.get("limit") ?? 50);
      const items = list.slice(0, limit);
      return HttpResponse.json({
        items,
        ...(list.length > limit ? { next_before: items[items.length - 1].id } : {}),
      });
    }),
  ];
}
