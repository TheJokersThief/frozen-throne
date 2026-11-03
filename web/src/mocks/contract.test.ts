/**
 * Contract test: proves the MSW mock backend (handlers.ts + db.ts) conforms to
 * api/openapi.yaml, and mirrors the behaviour of the real Go API
 * (internal/api) where the spec is silent, so the mocks cannot drift.
 *
 * The spec is parsed and used directly: every response is validated against
 * the declared response schema (with undeclared properties rejected), every
 * status must be one the spec declares, and request bodies the test sends are
 * validated against the declared request schema.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import Ajv, { type ValidateFunction } from "ajv";
import addFormats from "ajv-formats";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeEach, describe, expect, it } from "vitest";
import { parse } from "yaml";
import { MockDb } from "./db";
import { createHandlers } from "./handlers";

/* ------------------------------------------------------------------ spec */

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any;
const spec: Json = parse(readFileSync(resolve(process.cwd(), "../api/openapi.yaml"), "utf8"));

function resolveRef(ref: string): Json {
  if (!ref.startsWith("#/")) throw new Error(`unsupported $ref ${ref}`);
  return ref
    .slice(2)
    .split("/")
    .reduce((n: Json, k) => {
      const v = n?.[k.replace(/~1/g, "/").replace(/~0/g, "~")];
      if (v === undefined) throw new Error(`unresolvable $ref ${ref}`);
      return v;
    }, spec);
}

/**
 * Fully dereferences a (3.0 or 3.1) schema into plain JSON Schema: resolves
 * $ref, merges allOf, maps `nullable`, and (strict) forbids undeclared
 * properties on objects that declare `properties`.
 */
function toSchema(node: Json, strict: boolean): Json {
  if (node === null || typeof node !== "object") return node;
  if (Array.isArray(node)) return node.map((n) => toSchema(n, strict));
  if (node.$ref) return toSchema(resolveRef(node.$ref), strict);
  let out: Json = {};
  const { allOf, ...rest } = node;
  if (allOf) {
    out = { type: "object", properties: {}, required: [] };
    for (const m of [...allOf.map((x: Json) => toSchema(x, strict)), toSchema({ ...rest, allOf: undefined }, strict)]) {
      Object.assign(out.properties, m.properties ?? {});
      out.required.push(...(m.required ?? []));
    }
  } else {
    for (const [k, v] of Object.entries(rest)) {
      if (k === "properties") {
        out.properties = Object.fromEntries(Object.entries(v as Json).map(([p, s]) => [p, toSchema(s, strict)]));
      } else if (k === "items" || k === "additionalProperties") {
        out[k] = typeof v === "object" ? toSchema(v, strict) : v;
      } else if (["example", "description", "externalDocs"].includes(k)) {
        // annotation only
      } else {
        out[k] = v;
      }
    }
  }
  if (out.nullable && typeof out.type === "string") out.type = [out.type, "null"];
  delete out.nullable;
  if (strict && out.properties && out.additionalProperties === undefined) out.additionalProperties = false;
  return out;
}

const ajv = new Ajv({ strict: false, allErrors: true });
addFormats(ajv);
ajv.addFormat("int64", { type: "number", validate: (n: number) => Number.isInteger(n) });
ajv.addFormat("int32", { type: "number", validate: (n: number) => Number.isInteger(n) });
const compile = (s: Json): ValidateFunction => ajv.compile(toSchema(s, true));

interface Param { name: string; in: string; required: boolean; schema: Json }
interface Response { status: number; isJson: boolean; contentType?: string; validate?: ValidateFunction }
interface Op {
  key: string; method: string; path: string; id: string; authed: boolean;
  params: Param[]; responses: Map<number, Response>; request?: ValidateFunction;
}

const METHODS = ["get", "post", "put", "delete", "patch"];
const ops: Op[] = [];
for (const [path, item] of Object.entries<Json>(spec.paths)) {
  for (const method of METHODS) {
    const o = item[method];
    if (!o) continue;
    const deref = (x: Json): Json => (x.$ref ? resolveRef(x.$ref) : x);
    const params: Param[] = [...(item.parameters ?? []), ...(o.parameters ?? [])].map(deref).map((p: Json) => ({
      name: p.name, in: p.in, required: !!p.required, schema: toSchema(p.schema, false),
    }));
    const responses = new Map<number, Response>();
    for (const [code, r0] of Object.entries<Json>(o.responses)) {
      const r = deref(r0);
      const [contentType, media] = Object.entries<Json>(r.content ?? {})[0] ?? [];
      const isJson = contentType === "application/json";
      responses.set(Number(code), {
        status: Number(code), isJson, contentType,
        validate: isJson ? compile(media.schema) : undefined,
      });
    }
    const reqMedia = o.requestBody?.content?.["application/json"];
    ops.push({
      key: `${method.toUpperCase()} ${path}`, method: method.toUpperCase(), path, id: o.operationId,
      authed: (o.security ?? spec.security ?? []).length > 0, params, responses,
      request: reqMedia ? compile(reqMedia.schema) : undefined,
    });
  }
}
const opByKey = new Map(ops.map((o) => [o.key, o]));
const errorSchema = compile({ $ref: "#/components/schemas/Error" });
const ERROR_CODE: Record<number, string> = {
  400: "bad_request", 401: "unauthorized", 403: "forbidden", 404: "not_found", 409: "conflict",
};

/* ------------------------------------------------------------ exclusions */

/** Spec operations the mock deliberately does not implement. */
const EXCLUDED_OPS: Record<string, string> = {
  "GET /auth/login": "OAuth redirect to github.com (302); the mock stands in with POST /auth/mock-login",
  "GET /auth/callback": "OAuth redirect round-trip with GitHub (302 + Set-Cookie); not mockable in-process",
  "GET /orgs/{org}/slack": "Go API stubs every Slack endpoint with 501 (not in spec); mock has no Slack UI",
  "PUT /orgs/{org}/slack": "see GET /orgs/{org}/slack",
  "DELETE /orgs/{org}/slack": "see GET /orgs/{org}/slack",
  "POST /orgs/{org}/slack/test": "see GET /orgs/{org}/slack",
};
/** Mock-only routes that are intentionally not in the spec. */
const EXTRA_MOCK_ROUTES = new Set(["POST /auth/mock-login"]);

/**
 * Statuses the real Go API returns that the spec does not declare for the
 * operation (spec omissions, reported separately). They are tolerated, but
 * their body must still be a valid Error.
 */
const SPEC_GAPS: Record<string, number[]> = {
  "POST /auth/logout": [403], // CSRF header is enforced on every non-GET request
  "GET /repos": [400], // invalid limit/offset/state
  "GET /repos/{owner}/{repo}/bypasses": [400], // invalid limit/active
  "GET /audit": [400, 404], // invalid filters; org/repo not visible
  "POST /repos/{owner}/{repo}/freeze": [400], // reason too long / bad JSON / bad Content-Type
  "POST /orgs/{org}/freeze": [400],
  "PUT /repos/{owner}/{repo}/public-status": [400], // missing enabled / bad JSON
  "DELETE /repos/{owner}/{repo}/pulls/{number}/bypass": [400], // number < 1
  "DELETE /schedules/{id}": [],
};

/* -------------------------------------------------------------- harness */

const NOW = Date.parse("2026-03-04T12:00:00Z"); // a Wednesday
const BASE = "http://localhost/api/v1";

let db: MockDb;
let server: ReturnType<typeof setupServer>;
beforeEach(() => {
  db = new MockDb(() => NOW).seed();
  server = setupServer(...createHandlers(db));
  server.listen({ onUnhandledRequest: "error" });
});
afterEach(() => server.close());

const hits = new Map<string, Set<number>>();

interface Opts {
  params?: Record<string, string | number>;
  query?: Record<string, string | number | boolean>;
  body?: unknown;
  /** Send this verbatim instead of JSON.stringify(body). */
  raw?: string;
  csrf?: boolean;
  contentType?: string | null;
  /**
   * How the body relates to the spec request schema:
   * valid (default): must validate; schema-invalid: must NOT validate;
   * semantic: valid per schema but rejected by business rules; unchecked.
   */
  bodyIs?: "valid" | "schema-invalid" | "semantic" | "unchecked";
}
interface Result { status: number; body: Json; headers: Headers; text: string }

async function call(key: string, o: Opts = {}): Promise<Result> {
  const op = opByKey.get(key);
  if (!op) throw new Error(`unknown operation ${key}`);
  const path = op.path.replace(/\{(\w+)\}/g, (_, n) => {
    const v = o.params?.[n];
    if (v === undefined) throw new Error(`missing path param ${n} for ${key}`);
    return encodeURIComponent(String(v));
  });
  const qs = new URLSearchParams(Object.entries(o.query ?? {}).map(([k, v]) => [k, String(v)])).toString();
  const hasBody = o.body !== undefined || o.raw !== undefined;
  const headers: Record<string, string> = {};
  if (o.csrf !== false && op.method !== "GET") headers["X-Requested-With"] = "frozen-throne";
  if (hasBody && o.contentType !== null) headers["Content-Type"] = o.contentType ?? "application/json";

  if (o.body !== undefined && op.request) {
    const kind = o.bodyIs ?? "valid";
    const ok = op.request(o.body);
    if (kind === "valid" && !ok) throw new Error(`${key}: test sent a body that violates the spec: ${ajv.errorsText(op.request.errors)}`);
    if (kind === "schema-invalid") expect(ok, `${key}: body was expected to violate the spec schema`).toBe(false);
  }

  const res = await fetch(`${BASE}${path}${qs ? `?${qs}` : ""}`, {
    method: op.method, headers, body: hasBody ? (o.raw ?? JSON.stringify(o.body)) : undefined,
  });
  const text = await res.text();
  const declared = op.responses.get(res.status);
  const gap = (SPEC_GAPS[key] ?? []).includes(res.status);
  expect(declared || gap, `${key}: status ${res.status} is not declared by the spec (body: ${text})`).toBeTruthy();
  const set = hits.get(key) ?? new Set<number>();
  set.add(res.status);
  hits.set(key, set);

  let body: Json = undefined;
  if (declared?.isJson || gap) {
    expect(res.headers.get("content-type") ?? "", `${key} ${res.status} content-type`).toMatch(/^application\/json/);
    body = JSON.parse(text);
    const validate = declared?.validate ?? errorSchema;
    expect(validate(body), `${key} ${res.status} body violates the spec: ${ajv.errorsText(validate.errors)}\n${text}`).toBe(true);
  } else if (declared?.contentType) {
    expect(res.headers.get("content-type") ?? "", `${key} ${res.status} content-type`).toContain(declared.contentType);
  } else {
    expect(text, `${key} ${res.status} must have no body`).toBe("");
  }
  if (res.status >= 400 && body) {
    expect(body.code, `${key} ${res.status} error code`).toBe(ERROR_CODE[res.status]);
    expect(String(body.message).length).toBeGreaterThan(0);
  }
  return { status: res.status, body, headers: res.headers, text };
}

const err = (r: Result, status: number, fields?: string[]) => {
  expect(r.status, r.text).toBe(status);
  if (fields) expect(Object.keys(r.body.fields ?? {}).sort()).toEqual([...fields].sort());
};

/* ----------------------------------------------------- route coverage */

describe("route coverage (spec <-> mock)", () => {
  const mockRoutes = createHandlers(new MockDb().seed()).map((h) => {
    const info = h.info as unknown as { method: string; path: string };
    return `${info.method.toUpperCase()} ${String(info.path).replace(/^\*\/api\/v1/, "").replace(/:(\w+)/g, "{$1}")}`;
  });

  it("every exclusion names an operation that exists in the spec", () => {
    for (const k of Object.keys(EXCLUDED_OPS)) expect(opByKey.has(k), k).toBe(true);
    for (const k of Object.keys(SPEC_GAPS)) expect(opByKey.has(k), k).toBe(true);
  });

  it("every spec operation (minus exclusions) is handled by a mock route", () => {
    const missing = ops.map((o) => o.key).filter((k) => !(k in EXCLUDED_OPS) && !mockRoutes.includes(k));
    expect(missing).toEqual([]);
  });

  it("every mock route exists in the spec (minus the allowlist)", () => {
    const extra = mockRoutes.filter((k) => !opByKey.has(k) && !EXTRA_MOCK_ROUTES.has(k));
    expect(extra).toEqual([]);
  });

  it("excluded operations are really not mocked", () => {
    for (const k of Object.keys(EXCLUDED_OPS)) expect(mockRoutes, k).not.toContain(k);
  });
});

/* ------------------------------------------- derived: auth, CSRF, params */

describe("auth and CSRF (derived from the spec's operations)", () => {
  const sample = () => ({
    owner: "acme", repo: "web", org: "acme", number: 7, id: db.schedules[0].id,
  });
  const live = ops.filter((o) => !(o.key in EXCLUDED_OPS));

  for (const op of live.filter((o) => o.method !== "GET")) {
    it(`${op.key} without X-Requested-With is 403 forbidden (checked before auth)`, async () => {
      for (const loggedIn of [true, false]) {
        db.loggedIn = loggedIn;
        const r = await call(op.key, { params: sample(), csrf: false, body: op.request ? {} : undefined, bodyIs: "unchecked" });
        err(r, 403);
        expect(r.body.code).toBe("forbidden");
      }
    });
  }
  for (const op of live.filter((o) => o.authed)) {
    it(`${op.key} is 401 unauthorized when logged out`, async () => {
      db.loggedIn = false;
      const r = await call(op.key, { params: sample(), body: op.request ? {} : undefined, bodyIs: "unchecked" });
      err(r, 401);
      expect(r.body.code).toBe("unauthorized");
    });
  }
  for (const op of live.filter((o) => !o.authed && o.method === "GET")) {
    it(`${op.key} needs no session`, async () => {
      db.loggedIn = false;
      const r = await call(op.key, { params: { ...sample(), owner: "acme", repo: "docs" } });
      expect([200, 404]).toContain(r.status);
    });
  }

  it("a non-JSON Content-Type on a request with a body is rejected 400", async () => {
    const r = await call("POST /repos/{owner}/{repo}/freeze", {
      params: { owner: "acme", repo: "web" }, raw: '{"reason":"x"}', contentType: "text/plain", bodyIs: "unchecked",
    });
    err(r, 400);
    expect(db.repos.find((x) => x.name === "web")!.freeze).toBeUndefined();
  });
  it("malformed JSON is rejected 400", async () => {
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: { owner: "acme", repo: "web" }, raw: "{nope", bodyIs: "unchecked" }), 400);
  });
});

describe("query parameters are enforced as the spec declares them", () => {
  const sampleParams = { owner: "acme", repo: "web", org: "acme" };
  const getsWithQuery = ops.filter((o) => o.method === "GET" && o.params.some((p) => p.in === "query") && !(o.key in EXCLUDED_OPS));

  for (const op of getsWithQuery) {
    for (const p of op.params.filter((x) => x.in === "query")) {
      const s = p.schema;
      if (s.type === "integer" && (s.minimum !== undefined || s.maximum !== undefined)) {
        it(`${op.key} ?${p.name} accepts the bounds and rejects out-of-range / non-integers with fields.${p.name}`, async () => {
          for (const ok of [s.minimum, s.maximum].filter((v) => v !== undefined)) {
            err(await call(op.key, { params: sampleParams, query: { [p.name]: ok } }), 200);
          }
          const bad = [s.minimum !== undefined ? s.minimum - 1 : undefined, s.maximum !== undefined ? s.maximum + 1 : undefined, "abc"];
          for (const v of bad.filter((x) => x !== undefined)) {
            err(await call(op.key, { params: sampleParams, query: { [p.name]: v as string | number } }), 400, [p.name]);
          }
        });
      }
      if (s.enum) {
        it(`${op.key} ?${p.name} accepts every enum value and rejects others with fields.${p.name}`, async () => {
          for (const v of s.enum) err(await call(op.key, { params: sampleParams, query: { [p.name]: v } }), 200);
          err(await call(op.key, { params: sampleParams, query: { [p.name]: "bogus" } }), 400, [p.name]);
        });
      }
      if (s.type === "boolean") {
        it(`${op.key} ?${p.name} accepts true/false and rejects other values`, async () => {
          for (const v of [true, false]) err(await call(op.key, { params: sampleParams, query: { [p.name]: v } }), 200);
          err(await call(op.key, { params: sampleParams, query: { [p.name]: "maybe" } }), 400, [p.name]);
        });
      }
    }
  }
});

/* ------------------------------------------------------------------ auth */

describe("health and session", () => {
  it("GET /healthz", async () => {
    const r = await call("GET /healthz");
    err(r, 200);
    expect(r.body).toEqual({ status: "ok" });
  });
  it("GET /auth/me returns the user", async () => {
    const r = await call("GET /auth/me");
    err(r, 200);
    expect(r.body.login).toBe("octocat");
  });
  it("POST /auth/logout ends the session and is idempotent without one", async () => {
    err(await call("POST /auth/logout"), 204);
    err(await call("GET /auth/me"), 401);
    err(await call("POST /auth/logout"), 204);
  });
});

/* ----------------------------------------------------------------- repos */

describe("repos", () => {
  const repo = (name: string, owner = "acme") => ({ owner, repo: name });

  it("GET /repos lists only visible repos with permissions", async () => {
    const r = await call("GET /repos");
    err(r, 200);
    expect(r.body.total).toBe(db.visibleRepos().length);
    expect(r.body.items.map((i: Json) => i.full_name)).not.toContain("globex/secrets");
    const web = r.body.items.find((i: Json) => i.full_name === "acme/web");
    expect(web.permissions).toMatchObject({ can_freeze: true, can_thaw: true, can_bypass: true, can_manage_schedules: true });
    expect(web.permissions.can_manage_slack).toBeUndefined();
    const dash = r.body.items.find((i: Json) => i.full_name === "globex/dashboard");
    expect(dash.permissions).toMatchObject({ can_freeze: false, can_bypass: false });
  });

  it("GET /repos filters q (case-insensitive substring of owner/name), org and state", async () => {
    const all = (await call("GET /repos", { query: { limit: 200 } })).body;
    const q = await call("GET /repos", { query: { q: "ACME/W" } });
    expect(q.body.items.map((i: Json) => i.full_name)).toEqual(["acme/web"]);
    const org = await call("GET /repos", { query: { org: "globex" } });
    expect(org.body.items.every((i: Json) => i.owner === "globex")).toBe(true);
    expect(org.body.total).toBe(2);
    const frozen = (await call("GET /repos", { query: { state: "frozen" } })).body;
    const thawed = (await call("GET /repos", { query: { state: "thawed" } })).body;
    expect(frozen.items.length).toBeGreaterThan(0);
    expect(frozen.items.every((i: Json) => i.state.frozen)).toBe(true);
    expect(thawed.items.every((i: Json) => !i.state.frozen)).toBe(true);
    expect(frozen.total + thawed.total).toBe(all.total);
    const none = await call("GET /repos", { query: { org: "nope" } });
    expect(none.body).toEqual({ items: [], total: 0 });
  });

  it("GET /repos paginates with limit/offset and a stable total", async () => {
    const all = (await call("GET /repos", { query: { limit: 200 } })).body;
    const p1 = (await call("GET /repos", { query: { limit: 7, offset: 0 } })).body;
    const p2 = (await call("GET /repos", { query: { limit: 7, offset: 7 } })).body;
    expect(p1.items).toHaveLength(7);
    expect([...p1.items, ...p2.items]).toEqual(all.items.slice(0, 14));
    expect(p1.total).toBe(all.total);
    expect(p2.total).toBe(all.total);
    const past = (await call("GET /repos", { query: { offset: 500 } })).body;
    expect(past.items).toEqual([]);
    expect(past.total).toBe(all.total);
    expect((await call("GET /repos")).body.items.length).toBe(Math.min(50, all.total)); // default limit 50
  });

  it("GET /repos/{owner}/{repo} returns detail for every visible repo", async () => {
    for (const r of db.visibleRepos()) {
      const res = await call("GET /repos/{owner}/{repo}", { params: repo(r.name, r.owner) });
      err(res, 200);
      expect(res.body.full_name).toBe(`${r.owner}/${r.name}`);
      expect(res.body.default_branch).toBe("main");
    }
  });
  it("GET detail includes repo and org-wide schedules and active bypasses only", async () => {
    const api = (await call("GET /repos/{owner}/{repo}", { params: repo("api") })).body;
    expect(api.bypasses).toHaveLength(1);
    expect(api.state.reasons.map((x: Json) => x.kind)).toContain("repo_freeze");
    const mobile = (await call("GET /repos/{owner}/{repo}", { params: repo("mobile") })).body;
    expect(mobile.schedules.map((s: Json) => s.scope).sort()).toEqual(["org", "org", "repo"]);
    expect(mobile.state.reasons.map((x: Json) => x.kind)).toEqual(["schedule"]);
    expect(mobile.state.next_change).toBeTruthy();
  });
  it("GET detail is 404 for unknown and for invisible repos (never 403)", async () => {
    err(await call("GET /repos/{owner}/{repo}", { params: repo("secrets", "globex") }), 404);
    err(await call("GET /repos/{owner}/{repo}", { params: repo("nope") }), 404);
  });

  it("POST freeze creates a repo freeze (201) and 409s on a second one", async () => {
    const r = await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web"), body: { reason: "  Release cut  " } });
    err(r, 201);
    expect(r.body.rechecking).toBe(7);
    const reason = r.body.state.reasons.find((x: Json) => x.kind === "repo_freeze");
    expect(reason).toMatchObject({ text: "Release cut", by: "octocat" });
    expect(typeof reason.freeze_id).toBe("number");
    const detail = (await call("GET /repos/{owner}/{repo}", { params: repo("web") })).body;
    expect(detail.state).toEqual(r.body.state);
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web"), body: {} }), 409);
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("api"), body: {} }), 409);
  });
  it("POST freeze accepts an empty body and {} (reason is optional)", async () => {
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web") }), 201);
    const r = await call("POST /repos/{owner}/{repo}/freeze", { params: repo("mobile"), body: {} });
    err(r, 201);
    expect(r.body.state.reasons.find((x: Json) => x.kind === "repo_freeze").text).toBeUndefined();
  });
  it("POST freeze validates the body: reason length and unknown fields", async () => {
    const long = await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web"), body: { reason: "x".repeat(501) }, bodyIs: "schema-invalid" });
    err(long, 400, ["reason"]);
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web"), body: { reason: "x", extra: 1 }, bodyIs: "schema-invalid" }), 400);
    expect((await call("POST /repos/{owner}/{repo}/freeze", { params: repo("web"), body: { reason: "x".repeat(500) } })).status).toBe(201);
  });
  it("POST freeze: 403 for a member, 404 for an invisible repo (not 403)", async () => {
    const forbidden = await call("POST /repos/{owner}/{repo}/freeze", { params: repo("dashboard", "globex"), body: {} });
    err(forbidden, 403);
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("secrets", "globex"), body: {} }), 404);
    err(await call("POST /repos/{owner}/{repo}/freeze", { params: repo("nope"), body: {} }), 404);
  });

  it("DELETE freeze thaws, ends bypasses once fully thawed, and audits", async () => {
    const r = await call("DELETE /repos/{owner}/{repo}/freeze", { params: repo("api") });
    err(r, 200);
    expect(r.body.state.frozen).toBe(false);
    expect(r.body.rechecking).toBe(12);
    const bypasses = (await call("GET /repos/{owner}/{repo}/bypasses", { params: repo("api"), query: { active: false } })).body.items;
    expect(bypasses[0]).toMatchObject({ pr_number: 1873, end_reason: "thawed" });
    expect(bypasses[0].ended_at).toBeTruthy();
    const audit = (await call("GET /audit", { query: { action: "thaw", repo: "acme/api" } })).body.items;
    expect(audit[0]).toMatchObject({ action: "thaw", actor: "octocat" });
  });
  it("DELETE freeze: the repo stays frozen while an org freeze applies and bypasses survive", async () => {
    err(await call("POST /orgs/{org}/freeze", { params: { org: "acme" }, body: {} }), 201);
    const r = await call("DELETE /repos/{owner}/{repo}/freeze", { params: repo("api") });
    err(r, 200);
    expect(r.body.state.frozen).toBe(true);
    expect(r.body.state.reasons.map((x: Json) => x.kind)).toEqual(["org_freeze"]);
    const active = (await call("GET /repos/{owner}/{repo}/bypasses", { params: repo("api") })).body.items;
    expect(active).toHaveLength(1);
  });
  it("DELETE freeze: permission is checked before existence (403 for a member), then 404", async () => {
    err(await call("DELETE /repos/{owner}/{repo}/freeze", { params: repo("dashboard", "globex") }), 403);
    err(await call("DELETE /repos/{owner}/{repo}/freeze", { params: repo("web") }), 404); // not frozen
    err(await call("DELETE /repos/{owner}/{repo}/freeze", { params: repo("secrets", "globex") }), 404);
  });

  it("PUT public-status toggles opt-in", async () => {
    const r = await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("web"), body: { enabled: true } });
    err(r, 200);
    expect(r.body).toEqual({ enabled: true });
    expect((await call("GET /repos/{owner}/{repo}", { params: repo("web") })).body.public_status).toBe(true);
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("web"), body: { enabled: false } }), 200);
    const entry = (await call("GET /audit", { query: { action: "public_status.update" } })).body.items[0];
    expect(entry).toMatchObject({ repo: "acme/web", actor: "octocat", details: { enabled: false } });
  });
  it("PUT public-status validates the body and permissions", async () => {
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("web"), body: {}, bodyIs: "schema-invalid" }), 400, ["enabled"]);
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("web"), body: { enabled: "yes" }, bodyIs: "schema-invalid" }), 400);
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("web"), raw: "", bodyIs: "unchecked" }), 400);
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("dashboard", "globex"), body: { enabled: true } }), 403);
    err(await call("PUT /repos/{owner}/{repo}/public-status", { params: repo("secrets", "globex"), body: { enabled: true } }), 404);
  });
});

/* ------------------------------------------------------------------ orgs */

describe("orgs", () => {
  const org = (o: string) => ({ org: o });

  it("GET /orgs lists orgs with permissions incl. can_manage_slack", async () => {
    const r = await call("GET /orgs");
    err(r, 200);
    expect(r.body.items.map((o: Json) => o.login)).toEqual(["acme", "globex", "octocat"]);
    expect(r.body.items[0].permissions).toMatchObject({ can_freeze: true, can_manage_slack: true });
    expect(r.body.items[1].permissions).toMatchObject({ can_freeze: false, can_bypass: false, can_manage_slack: false });
    expect(r.body.items[0].repo_count).toBe(16);
  });
  it("GET /orgs/{org}", async () => {
    err(await call("GET /orgs/{org}", { params: org("acme") }), 200);
    err(await call("GET /orgs/{org}", { params: org("nope") }), 404);
  });

  it("POST /orgs/{org}/freeze freezes every repo, 201, then 409", async () => {
    const r = await call("POST /orgs/{org}/freeze", { params: org("acme"), body: { reason: "Holiday" } });
    err(r, 201);
    expect(r.body.org.state.reasons[0]).toMatchObject({ kind: "org_freeze", text: "Holiday", by: "octocat" });
    const open = db.repos.filter((x) => x.owner === "acme").reduce((a, x) => a + x.open_prs, 0);
    expect(r.body.rechecking).toBe(open);
    expect((await call("GET /repos/{owner}/{repo}", { params: { owner: "acme", repo: "web" } })).body.state.frozen).toBe(true);
    err(await call("POST /orgs/{org}/freeze", { params: org("acme"), body: {} }), 409);
  });
  it("POST /orgs/{org}/freeze accepts an empty body, validates reason, enforces roles", async () => {
    err(await call("POST /orgs/{org}/freeze", { params: org("octocat") }), 201);
    err(await call("POST /orgs/{org}/freeze", { params: org("acme"), body: { reason: "x".repeat(501) }, bodyIs: "schema-invalid" }), 400, ["reason"]);
    err(await call("POST /orgs/{org}/freeze", { params: org("globex"), body: {} }), 403);
    err(await call("POST /orgs/{org}/freeze", { params: org("nope"), body: {} }), 404);
  });
  it("DELETE /orgs/{org}/freeze thaws; repos with their own freeze stay frozen", async () => {
    await call("POST /orgs/{org}/freeze", { params: org("acme"), body: {} });
    const r = await call("DELETE /orgs/{org}/freeze", { params: org("acme") });
    err(r, 200);
    expect(r.body.org.state.frozen).toBe(false);
    expect((await call("GET /repos/{owner}/{repo}", { params: { owner: "acme", repo: "api" } })).body.state.frozen).toBe(true);
    expect((await call("GET /repos/{owner}/{repo}", { params: { owner: "acme", repo: "web" } })).body.state.frozen).toBe(false);
  });
  it("DELETE /orgs/{org}/freeze checks permission before existence", async () => {
    err(await call("DELETE /orgs/{org}/freeze", { params: org("globex") }), 403);
    err(await call("DELETE /orgs/{org}/freeze", { params: org("acme") }), 404);
    err(await call("DELETE /orgs/{org}/freeze", { params: org("nope") }), 404);
  });
});

/* ------------------------------------------------------------- schedules */

const oneOff = (over: Json = {}): Json => ({
  kind: "one_off", name: "Release", reason: "Cut", timezone: "UTC",
  starts_at: "2026-03-05T00:00:00Z", ends_at: "2026-03-06T00:00:00Z", ...over,
});
const recurring = (over: Json = {}): Json => ({
  kind: "recurring", name: "Nights", timezone: "Europe/Dublin", weekdays: ["fri", "mon"],
  start_time: "17:00", end_time: "09:00", ...over,
});

describe("schedules", () => {
  const acme = { org: "acme" };
  const web = { owner: "acme", repo: "web" };

  it("lists org-wide and repo-scoped windows separately", async () => {
    const o = (await call("GET /orgs/{org}/schedules", { params: acme })).body.items;
    expect(o.map((s: Json) => s.scope)).toEqual(["org", "org"]);
    const r = (await call("GET /repos/{owner}/{repo}/schedules", { params: { owner: "acme", repo: "mobile" } })).body.items;
    expect(r.map((s: Json) => [s.scope, s.repo])).toEqual([["repo", "acme/mobile"]]);
    expect(r[0]).toMatchObject({ active: true });
    expect(r[0].next_end).toBeTruthy();
    expect((await call("GET /orgs/{org}/schedules", { params: { org: "globex" } })).body.items).toEqual([]);
    err(await call("GET /orgs/{org}/schedules", { params: { org: "nope" } }), 404);
    err(await call("GET /repos/{owner}/{repo}/schedules", { params: { owner: "globex", repo: "secrets" } }), 404);
  });

  it("creates org one_off and recurring windows (201) with server-managed fields", async () => {
    const a = await call("POST /orgs/{org}/schedules", { params: acme, body: oneOff() });
    err(a, 201);
    expect(a.body).toMatchObject({ scope: "org", created_by: "octocat", enabled: true, kind: "one_off", active: false });
    expect(a.body.repo).toBeUndefined();
    expect(a.body.next_start).toBe("2026-03-05T00:00:00Z");
    const b = await call("POST /orgs/{org}/schedules", { params: acme, body: recurring({ valid_from: "2026-01-01", valid_until: "2026-12-31" }) });
    err(b, 201);
    expect(b.body.weekdays).toEqual(["mon", "fri"]);
    const list = (await call("GET /orgs/{org}/schedules", { params: acme })).body.items.map((s: Json) => s.id);
    expect(list).toEqual(expect.arrayContaining([a.body.id, b.body.id]));
    const got = await call("GET /schedules/{id}", { params: { id: a.body.id } });
    err(got, 200);
    expect(got.body).toEqual(a.body);
  });
  it("only persists the fields that belong to the schedule kind", async () => {
    const r = await call("POST /orgs/{org}/schedules", { params: acme, body: oneOff({ weekdays: ["mon"], start_time: "01:00", end_time: "02:00" }) });
    err(r, 201);
    expect(r.body.weekdays).toBeUndefined();
    expect(r.body.start_time).toBeUndefined();
    const rec = await call("POST /orgs/{org}/schedules", { params: acme, body: recurring({ starts_at: "2026-03-05T00:00:00Z", ends_at: "2026-03-06T00:00:00Z" }) });
    err(rec, 201);
    expect(rec.body.starts_at).toBeUndefined();
  });
  it("an active one-off repo window freezes the repo; deleting it thaws", async () => {
    const s = await call("POST /repos/{owner}/{repo}/schedules", {
      params: web, body: oneOff({ starts_at: "2026-03-04T10:00:00Z", ends_at: "2026-03-04T14:00:00Z" }),
    });
    err(s, 201);
    expect(s.body).toMatchObject({ scope: "repo", repo: "acme/web", active: true, next_end: "2026-03-04T14:00:00Z" });
    const frozen = (await call("GET /repos/{owner}/{repo}", { params: web })).body.state;
    expect(frozen.reasons[0]).toMatchObject({ kind: "schedule", schedule_id: s.body.id, until: "2026-03-04T14:00:00Z" });
    const del = await call("DELETE /schedules/{id}", { params: { id: s.body.id } });
    err(del, 204);
    expect((await call("GET /repos/{owner}/{repo}", { params: web })).body.state.frozen).toBe(false);
    err(await call("GET /schedules/{id}", { params: { id: s.body.id } }), 404);
    err(await call("DELETE /schedules/{id}", { params: { id: s.body.id } }), 404);
  });
  it("a disabled window is never active and has no upcoming change", async () => {
    const r = await call("POST /orgs/{org}/schedules", { params: acme, body: oneOff({ enabled: false }) });
    err(r, 201);
    expect(r.body).toMatchObject({ enabled: false, active: false });
    expect(r.body.next_start).toBeUndefined();
  });

  const invalid: [string, Json, string[], boolean][] = [
    ["missing timezone", oneOff({ timezone: undefined }), ["timezone"], true],
    ["unknown timezone", oneOff({ timezone: "Mars/Olympus" }), ["timezone"], false],
    ["missing kind", oneOff({ kind: undefined }), ["kind"], true],
    ["unknown kind", oneOff({ kind: "weekly" }), ["kind"], true],
    ["one_off without starts_at", oneOff({ starts_at: undefined }), ["starts_at"], false],
    ["one_off without ends_at", oneOff({ ends_at: undefined }), ["ends_at"], false],
    ["one_off ends before it starts", oneOff({ ends_at: "2026-03-04T00:00:00Z" }), ["ends_at"], false],
    ["one_off with equal bounds", oneOff({ ends_at: "2026-03-05T00:00:00Z" }), ["ends_at"], false],
    ["name too long", oneOff({ name: "n".repeat(101) }), ["name"], true],
    ["reason too long", oneOff({ reason: "r".repeat(501) }), ["reason"], true],
    ["recurring without weekdays", recurring({ weekdays: undefined }), ["weekdays"], false],
    ["recurring with empty weekdays", recurring({ weekdays: [] }), ["weekdays"], true],
    ["recurring with an unknown weekday", recurring({ weekdays: ["xyz"] }), ["weekdays"], true],
    ["recurring with duplicate weekdays", recurring({ weekdays: ["mon", "mon"] }), ["weekdays"], true],
    ["recurring without start_time", recurring({ start_time: undefined }), ["start_time"], false],
    ["recurring with malformed times", recurring({ start_time: "5pm", end_time: "24:00" }), ["start_time", "end_time"], true],
    ["recurring with a malformed date", recurring({ valid_from: "03/04/2026" }), ["valid_from"], true],
    ["valid_until before valid_from", recurring({ valid_from: "2026-05-01", valid_until: "2026-04-01" }), ["valid_until"], false],
  ];
  for (const [name, body, fields, schemaInvalid] of invalid) {
    it(`rejects ${name} with 400 and fields (${fields.join(",")})`, async () => {
      const clean = JSON.parse(JSON.stringify(body));
      for (const [path, p] of [
        ["POST /orgs/{org}/schedules", acme],
        ["POST /repos/{owner}/{repo}/schedules", web],
      ] as const) {
        err(await call(path, { params: p, body: clean, bodyIs: schemaInvalid ? "schema-invalid" : "semantic" }), 400, fields);
      }
      const id = db.schedules[0].id;
      err(await call("PUT /schedules/{id}", { params: { id }, body: clean, bodyIs: schemaInvalid ? "schema-invalid" : "semantic" }), 400, fields);
    });
  }
  it("rejects unknown request fields and malformed JSON with 400", async () => {
    err(await call("POST /orgs/{org}/schedules", { params: acme, body: oneOff({ surprise: 1 }), bodyIs: "schema-invalid" }), 400);
    err(await call("POST /orgs/{org}/schedules", { params: acme, raw: "[1,2]", bodyIs: "unchecked" }), 400);
    err(await call("POST /orgs/{org}/schedules", { params: acme, raw: "", bodyIs: "unchecked" }), 400);
    err(await call("POST /orgs/{org}/schedules", { params: acme, body: oneOff({ weekdays: "mon" }), bodyIs: "schema-invalid" }), 400);
  });

  it("enforces roles on create: 403 for members, 404 for invisible or unknown scopes", async () => {
    err(await call("POST /orgs/{org}/schedules", { params: { org: "globex" }, body: oneOff() }), 403);
    err(await call("POST /orgs/{org}/schedules", { params: { org: "nope" }, body: oneOff() }), 404);
    err(await call("POST /repos/{owner}/{repo}/schedules", { params: { owner: "globex", repo: "dashboard" }, body: oneOff() }), 403);
    err(await call("POST /repos/{owner}/{repo}/schedules", { params: { owner: "globex", repo: "secrets" }, body: oneOff() }), 404);
    // permission is decided before the body is looked at
    err(await call("POST /orgs/{org}/schedules", { params: { org: "globex" }, body: {}, bodyIs: "schema-invalid" }), 403);
  });

  it("GET /schedules/{id} returns every seeded window; unknown/invalid/invisible ids are 404", async () => {
    for (const s of db.schedules) err(await call("GET /schedules/{id}", { params: { id: s.id } }), 200);
    err(await call("GET /schedules/{id}", { params: { id: 999999 } }), 404);
    err(await call("GET /schedules/{id}", { params: { id: "abc" } }), 404);
    const hidden = db.buildSchedule(oneOff(), "repo", "globex", "globex/secrets");
    db.schedules.push(hidden);
    err(await call("GET /schedules/{id}", { params: { id: hidden.id } }), 404);
  });

  it("PUT /schedules/{id} replaces mutable fields and keeps id, scope and creator", async () => {
    const created = (await call("POST /repos/{owner}/{repo}/schedules", { params: web, body: oneOff() })).body;
    const r = await call("PUT /schedules/{id}", { params: { id: created.id }, body: recurring({ name: "Renamed" }) });
    err(r, 200);
    expect(r.body).toMatchObject({ id: created.id, scope: "repo", repo: "acme/web", created_by: "octocat", created_at: created.created_at, kind: "recurring", name: "Renamed" });
    expect(r.body.starts_at).toBeUndefined();
    const audit = (await call("GET /audit", { query: { action: "schedule.update" } })).body.items;
    expect(audit[0]).toMatchObject({ repo: "acme/web", scope: "repo" });
  });
  it("PUT/DELETE /schedules/{id} roles: member 403, invisible 404 (before validation)", async () => {
    const mine = db.buildSchedule(oneOff(), "repo", "globex", "globex/dashboard");
    const hidden = db.buildSchedule(oneOff(), "repo", "globex", "globex/secrets");
    const orgWide = db.buildSchedule(oneOff(), "org", "globex");
    db.schedules.push(mine, hidden, orgWide);
    for (const s of [mine, orgWide]) {
      err(await call("PUT /schedules/{id}", { params: { id: s.id }, body: oneOff() }), 403);
      err(await call("DELETE /schedules/{id}", { params: { id: s.id } }), 403);
    }
    err(await call("PUT /schedules/{id}", { params: { id: hidden.id }, body: {}, bodyIs: "schema-invalid" }), 404);
    err(await call("DELETE /schedules/{id}", { params: { id: hidden.id } }), 404);
    err(await call("PUT /schedules/{id}", { params: { id: 999999 }, body: oneOff() }), 404);
  });
  it("deleting an active org window thaws and ends bypasses (audited)", async () => {
    const s = (await call("POST /orgs/{org}/schedules", { params: { org: "octocat" }, body: oneOff({ starts_at: "2026-03-04T10:00:00Z", ends_at: "2026-03-04T14:00:00Z" }) })).body;
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: { owner: "octocat", repo: "hello-world", number: 3 }, body: { reason: "needed" } }), 201);
    err(await call("DELETE /schedules/{id}", { params: { id: s.id } }), 204);
    const ended = (await call("GET /repos/{owner}/{repo}/bypasses", { params: { owner: "octocat", repo: "hello-world" }, query: { active: false } })).body.items;
    expect(ended[0].end_reason).toBe("thawed");
    const actions = (await call("GET /audit", { query: { limit: 200 } })).body.items.map((e: Json) => e.action);
    expect(actions).toEqual(expect.arrayContaining(["schedule.create", "schedule.delete", "bypass.end"]));
  });
});

/* ---------------------------------------------------------------- bypass */

describe("bypasses", () => {
  const api = { owner: "acme", repo: "api" };
  const at = (n: number | string, p = api) => ({ ...p, number: n });

  it("lists active bypasses by default; active=false includes ended; limit caps", async () => {
    const a = (await call("GET /repos/{owner}/{repo}/bypasses", { params: api })).body.items;
    expect(a).toHaveLength(1);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1900), body: { reason: "second" } }), 201);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873) }), 204);
    const active = (await call("GET /repos/{owner}/{repo}/bypasses", { params: api })).body.items;
    expect(active.map((b: Json) => b.pr_number)).toEqual([1900]);
    const all = (await call("GET /repos/{owner}/{repo}/bypasses", { params: api, query: { active: false } })).body.items;
    expect(all.map((b: Json) => b.pr_number)).toEqual([1900, 1873]); // newest first
    const one = (await call("GET /repos/{owner}/{repo}/bypasses", { params: api, query: { active: false, limit: 1 } })).body.items;
    expect(one.map((b: Json) => b.pr_number)).toEqual([1900]);
    err(await call("GET /repos/{owner}/{repo}/bypasses", { params: { owner: "globex", repo: "secrets" } }), 404);
  });
  it("members can list bypasses", async () => {
    err(await call("GET /repos/{owner}/{repo}/bypasses", { params: { owner: "globex", repo: "dashboard" } }), 200);
  });

  it("POST creates a bypass (201) with a trimmed reason, even on an unfrozen repo", async () => {
    const r = await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(42, { owner: "acme", repo: "web" }), body: { reason: "  hotfix  " } });
    err(r, 201);
    expect(r.body).toMatchObject({ repo: "acme/web", pr_number: 42, reason: "hotfix", created_by: "octocat" });
    expect(r.body.ended_at).toBeUndefined();
    const entry = (await call("GET /audit", { query: { action: "bypass.create" } })).body.items[0];
    expect(entry).toMatchObject({ repo: "acme/web", pr_number: 42, reason: "hotfix", actor: "octocat" });
  });
  it("POST 409s when the PR already has an active bypass, but allows a new one after revocation", async () => {
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873), body: { reason: "again" } }), 409);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873) }), 204);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873), body: { reason: "again" } }), 201);
  });
  it("POST validates reason (required, non-blank, <= 500), body shape and PR number", async () => {
    const p = at(5);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: {}, bodyIs: "schema-invalid" }), 400, ["reason"]);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: { reason: "   " }, bodyIs: "semantic" }), 400, ["reason"]);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: { reason: "" }, bodyIs: "schema-invalid" }), 400, ["reason"]);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: { reason: "x".repeat(501) }, bodyIs: "schema-invalid" }), 400, ["reason"]);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: { reason: "ok", extra: true }, bodyIs: "schema-invalid" }), 400);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, raw: "", bodyIs: "unchecked" }), 400);
    for (const n of [0, -1, "abc"]) {
      err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(n), body: { reason: "ok" } }), 400, ["number"]);
    }
    expect((await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: p, body: { reason: "x".repeat(500) } })).status).toBe(201);
  });
  it("POST/DELETE roles: 403 for members (before validation), 404 for invisible repos", async () => {
    const dash = { owner: "globex", repo: "dashboard" };
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1, dash), body: { reason: "x" } }), 403);
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1, dash), body: {}, bodyIs: "schema-invalid" }), 403);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1, dash) }), 403);
    const hidden = { owner: "globex", repo: "secrets" };
    err(await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1, hidden), body: { reason: "x" } }), 404);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1, hidden) }), 404);
  });
  it("DELETE revokes (204), then 404; 400 for a bad number", async () => {
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873) }), 204);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(1873) }), 404);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(777) }), 404);
    err(await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: at(0) }), 400, ["number"]);
    const e = (await call("GET /audit", { query: { action: "bypass.end" } })).body.items[0];
    expect(e).toMatchObject({ pr_number: 1873, actor: "octocat", details: { end_reason: "revoked" } });
    const ended = (await call("GET /repos/{owner}/{repo}/bypasses", { params: api, query: { active: false } })).body.items[0];
    expect(ended.end_reason).toBe("revoked");
  });
});

/* ----------------------------------------------------------------- audit */

describe("audit", () => {
  it("lists newest first, validated against the schema, without invisible repos", async () => {
    db.log("freeze", "globex", { repo: "globex/secrets", reason: "hidden" });
    const r = await call("GET /audit", { query: { limit: 200 } });
    err(r, 200);
    const ids = r.body.items.map((e: Json) => e.id);
    expect(ids).toEqual([...ids].sort((a, b) => b - a));
    expect(r.body.items.map((e: Json) => e.repo)).not.toContain("globex/secrets");
    expect(r.body.items.length).toBeGreaterThan(5);
    expect(r.body.next_before).toBeUndefined();
  });
  it("filters by org, repo, actor and action", async () => {
    const q = async (query: Record<string, string>) => (await call("GET /audit", { query: { limit: 200, ...query } })).body.items as Json[];
    expect((await q({ org: "globex" })).every((e) => e.org === "globex")).toBe(true);
    expect((await q({ repo: "acme/api" })).every((e) => e.repo === "acme/api")).toBe(true);
    expect((await q({ actor: "hubot" })).map((e) => e.actor)).toEqual(["hubot"]);
    expect((await q({ action: "bypass.end" })).every((e) => e.action === "bypass.end")).toBe(true);
    expect(await q({ org: "acme", action: "thaw", actor: "nobody" })).toEqual([]);
  });
  it("rejects unknown org/repo (404) and malformed filters (400)", async () => {
    err(await call("GET /audit", { query: { org: "nope" } }), 404);
    err(await call("GET /audit", { query: { repo: "globex/secrets" } }), 404);
    err(await call("GET /audit", { query: { repo: "acme/nope" } }), 404);
    err(await call("GET /audit", { query: { repo: "nope" } }), 400, ["repo"]);
    err(await call("GET /audit", { query: { before: "abc" } }), 400, ["before"]);
  });
  it("paginates with before = next_before until next_before is absent", async () => {
    const all = (await call("GET /audit", { query: { limit: 200 } })).body.items;
    expect(all.length).toBeGreaterThan(4);
    const seen: Json[] = [];
    let before: number | undefined;
    for (let i = 0; i < 50; i++) {
      const r = await call("GET /audit", { query: { limit: 2, ...(before ? { before } : {}) } });
      expect(r.body.items.length).toBeLessThanOrEqual(2);
      seen.push(...r.body.items);
      if (r.body.next_before === undefined) break;
      expect(r.body.next_before).toBe(r.body.items[r.body.items.length - 1].id);
      expect(r.body.items).toHaveLength(2);
      before = r.body.next_before;
    }
    expect(seen).toEqual(all);
  });
  it("records every mutation kind with schema-valid entries", async () => {
    await call("POST /repos/{owner}/{repo}/freeze", { params: { owner: "acme", repo: "web" }, body: { reason: "r" } });
    await call("DELETE /repos/{owner}/{repo}/freeze", { params: { owner: "acme", repo: "web" } });
    await call("POST /orgs/{org}/freeze", { params: { org: "acme" }, body: {} });
    await call("DELETE /orgs/{org}/freeze", { params: { org: "acme" } });
    const s = (await call("POST /orgs/{org}/schedules", { params: { org: "acme" }, body: oneOff() })).body;
    await call("PUT /schedules/{id}", { params: { id: s.id }, body: oneOff({ name: "n2" }) });
    await call("DELETE /schedules/{id}", { params: { id: s.id } });
    await call("POST /repos/{owner}/{repo}/pulls/{number}/bypass", { params: { owner: "acme", repo: "web", number: 9 }, body: { reason: "r" } });
    await call("DELETE /repos/{owner}/{repo}/pulls/{number}/bypass", { params: { owner: "acme", repo: "web", number: 9 } });
    await call("PUT /repos/{owner}/{repo}/public-status", { params: { owner: "acme", repo: "web" }, body: { enabled: true } });
    const actions = new Set((await call("GET /audit", { query: { limit: 200 } })).body.items.map((e: Json) => e.action));
    for (const a of ["freeze", "thaw", "schedule.create", "schedule.update", "schedule.delete", "bypass.create", "bypass.end", "public_status.update"]) {
      expect(actions.has(a), a).toBe(true);
    }
  });
});

/* ---------------------------------------------------------------- public */

describe("public endpoints", () => {
  const docs = { owner: "acme", repo: "docs" };

  it("status of an opted-in repo needs no session and no extra detail", async () => {
    db.loggedIn = false;
    const r = await call("GET /public/repos/{owner}/{repo}/status", { params: docs });
    err(r, 200);
    expect(r.body).toEqual({ repo: "acme/docs", frozen: false });
  });
  it("status shows since and (for windows only) until when frozen, never reasons or actors", async () => {
    await call("POST /repos/{owner}/{repo}/freeze", { params: docs, body: { reason: "secret reason" } });
    const manual = (await call("GET /public/repos/{owner}/{repo}/status", { params: docs })).body;
    expect(manual).toMatchObject({ repo: "acme/docs", frozen: true });
    expect(manual.since).toBeTruthy();
    expect(manual.until).toBeUndefined();
    expect(JSON.stringify(manual)).not.toContain("secret");
    await call("DELETE /repos/{owner}/{repo}/freeze", { params: docs });
    await call("POST /repos/{owner}/{repo}/schedules", { params: docs, body: oneOff({ starts_at: "2026-03-04T10:00:00Z", ends_at: "2026-03-04T14:00:00Z" }) });
    const win = (await call("GET /public/repos/{owner}/{repo}/status", { params: docs })).body;
    expect(win).toEqual({ repo: "acme/docs", frozen: true, since: "2026-03-04T10:00:00Z", until: "2026-03-04T14:00:00Z" });
  });
  it("not-opted-in and unknown repos give identical 404s", async () => {
    const a = await call("GET /public/repos/{owner}/{repo}/status", { params: { owner: "acme", repo: "web" } });
    const b = await call("GET /public/repos/{owner}/{repo}/status", { params: { owner: "acme", repo: "nope" } });
    err(a, 404);
    expect(a.text).toBe(b.text);
    await call("PUT /repos/{owner}/{repo}/public-status", { params: { owner: "acme", repo: "web" }, body: { enabled: true } });
    err(await call("GET /public/repos/{owner}/{repo}/status", { params: { owner: "acme", repo: "web" } }), 200);
  });
  it("badge is an SVG with a short cache lifetime; unknown repos get the generic 404 badge", async () => {
    db.loggedIn = false;
    const open = await call("GET /public/repos/{owner}/{repo}/badge.svg", { params: docs });
    err(open, 200);
    expect(open.text).toContain("<svg");
    expect(open.text).toContain("open");
    expect(open.headers.get("cache-control")).toBe("public, max-age=60");
    db.loggedIn = true;
    await call("POST /repos/{owner}/{repo}/freeze", { params: docs, body: {} });
    expect((await call("GET /public/repos/{owner}/{repo}/badge.svg", { params: docs })).text).toContain("frozen");
    const hidden = await call("GET /public/repos/{owner}/{repo}/badge.svg", { params: { owner: "acme", repo: "web" } });
    const nope = await call("GET /public/repos/{owner}/{repo}/badge.svg", { params: { owner: "acme", repo: "nope" } });
    err(hidden, 404);
    expect(hidden.text).toContain("unknown");
    expect(hidden.text).toBe(nope.text);
  });
});

/* ------------------------------------------------- every response covered */

describe("coverage of declared responses", () => {
  it("every declared status of every mocked operation was exercised by this file", () => {
    const missing: string[] = [];
    for (const op of ops) {
      if (op.key in EXCLUDED_OPS) continue;
      for (const status of op.responses.keys()) {
        if (!hits.get(op.key)?.has(status)) missing.push(`${op.key} -> ${status}`);
      }
    }
    expect(missing).toEqual([]);
  });
});

afterAll(() => hits.clear());
