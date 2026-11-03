import type {
  AuditAction, AuditEntry, Bypass, FreezeReason, FreezeState, Org, Permissions,
  RepoDetail, RepoSummary, Schedule, ScheduleInput, User,
} from "../api/types";
import { recurringActive } from "../lib/time";

type Role = "none" | "member" | "maintainer" | "owner";
interface ManualFreeze { id: number; reason?: string; by: string; since: string }

export interface MockRepo {
  id: number; owner: string; name: string; private: boolean; archived: boolean;
  open_prs: number; public_status: boolean; role: Role; freeze?: ManualFreeze;
}
export interface MockOrg {
  installation_id: number; login: string; type: "Organization" | "User"; role: Role;
  freeze?: ManualFreeze;
}
export type MockSchedule = Schedule & { org: string };

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

/** In-memory backend mirroring the OpenAPI semantics. Pure data, no HTTP. */
export class MockDb {
  user: User = { id: 1, login: "octocat", name: "Octo Cat" };
  loggedIn = true;
  orgs: MockOrg[] = [];
  repos: MockRepo[] = [];
  schedules: MockSchedule[] = [];
  bypasses: Bypass[] = [];
  audit: AuditEntry[] = [];
  private seq = 100;

  constructor(public now: () => number = Date.now) {}

  nextId() {
    return ++this.seq;
  }
  iso(ms = this.now()) {
    return new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
  }

  // ---- permissions ----
  private perms(role: Role, org: boolean): Permissions {
    const maint = role === "maintainer" || role === "owner";
    const owner = role === "owner";
    return {
      can_freeze: org ? owner : maint,
      can_thaw: org ? owner : maint,
      can_bypass: maint,
      can_manage_schedules: org ? owner : maint,
      ...(org ? { can_manage_slack: owner } : {}),
    };
  }
  /** Org owners are implicitly maintainers of every repo of their org. */
  repoRole(r: MockRepo): Role {
    const o = this.orgs.find((x) => x.login === r.owner)!;
    return o.role === "owner" ? "owner" : r.role;
  }
  findRepo(owner: string, name: string) {
    return this.repos.find((r) => r.owner === owner && r.name === name && this.repoRole(r) !== "none");
  }
  findOrg(login: string) {
    return this.orgs.find((o) => o.login === login);
  }

  // ---- state ----
  scheduleActive(s: Schedule): boolean {
    if (!s.enabled) return false;
    const t = this.now();
    if (s.kind === "one_off") return Date.parse(s.starts_at!) <= t && t < Date.parse(s.ends_at!);
    return recurringActive(s, t);
  }

  schedule(s: MockSchedule): Schedule {
    const active = this.scheduleActive(s);
    const { org: _org, ...out } = s;
    void _org;
    const res: Schedule = { ...out, active };
    if (s.kind === "one_off") {
      if (active) res.next_end = s.ends_at;
      else if (Date.parse(s.starts_at!) > this.now()) res.next_start = s.starts_at;
    }
    return res;
  }

  schedulesFor(r: MockRepo) {
    return this.schedules.filter(
      (s) => (s.scope === "org" && s.org === r.owner) || (s.scope === "repo" && s.repo === `${r.owner}/${r.name}`),
    );
  }

  private windowReason(s: Schedule): FreezeReason {
    return {
      kind: "schedule", schedule_id: s.id, text: s.name || s.reason || "Freeze window",
      since: s.starts_at ?? this.iso(this.now() - HOUR), until: s.ends_at ?? this.iso(this.now() + 2 * HOUR),
    };
  }
  private manual(kind: "org_freeze" | "repo_freeze", f: ManualFreeze): FreezeReason {
    return { kind, freeze_id: f.id, text: f.reason, by: f.by, since: f.since };
  }

  orgState(o: MockOrg): FreezeState {
    const reasons: FreezeReason[] = [];
    if (o.freeze) reasons.push(this.manual("org_freeze", o.freeze));
    for (const s of this.schedules.filter((x) => x.scope === "org" && x.org === o.login)) {
      if (this.scheduleActive(s)) reasons.push(this.windowReason(s));
    }
    return { frozen: reasons.length > 0, reasons };
  }

  repoState(r: MockRepo): FreezeState {
    const reasons: FreezeReason[] = [...this.orgState(this.findOrg(r.owner)!).reasons];
    if (r.freeze) reasons.push(this.manual("repo_freeze", r.freeze));
    for (const s of this.schedulesFor(r).filter((x) => x.scope === "repo")) {
      if (this.scheduleActive(s)) reasons.push(this.windowReason(s));
    }
    const upcoming = this.schedulesFor(r)
      .map((s) => this.schedule(s))
      .flatMap((s) => [s.next_start, s.next_end])
      .filter((x): x is string => !!x)
      .sort();
    return { frozen: reasons.length > 0, reasons, ...(upcoming[0] ? { next_change: upcoming[0] } : {}) };
  }

  /** Bypasses end when the repo stops being frozen. */
  sweepBypasses() {
    for (const r of this.repos) {
      if (this.repoState(r).frozen) continue;
      for (const b of this.bypasses) {
        if (b.repo === `${r.owner}/${r.name}` && !b.ended_at) {
          b.ended_at = this.iso();
          b.end_reason = "thawed";
          this.log("bypass.end", r.owner, { repo: b.repo, pr_number: b.pr_number, actor: undefined });
        }
      }
    }
  }

  activeBypasses(r: MockRepo) {
    return this.bypasses.filter((b) => b.repo === `${r.owner}/${r.name}` && !b.ended_at);
  }

  summary(r: MockRepo): RepoSummary {
    const state = this.repoState(r);
    return {
      id: r.id, owner: r.owner, name: r.name, full_name: `${r.owner}/${r.name}`,
      installation_id: this.findOrg(r.owner)!.installation_id,
      private: r.private, archived: r.archived, state, open_prs: r.open_prs,
      blocked_prs: state.frozen ? Math.max(0, r.open_prs - this.activeBypasses(r).length) : 0,
      public_status: r.public_status, permissions: this.perms(this.repoRole(r), false),
    };
  }
  detail(r: MockRepo): RepoDetail {
    return {
      ...this.summary(r), default_branch: "main",
      schedules: this.schedulesFor(r).map((s) => this.schedule(s)),
      bypasses: this.activeBypasses(r),
    };
  }
  orgOut(o: MockOrg): Org {
    return {
      installation_id: o.installation_id, login: o.login, type: o.type,
      repo_count: this.repos.filter((r) => r.owner === o.login).length,
      state: this.orgState(o), permissions: this.perms(o.role, true),
    };
  }
  visibleRepos() {
    return this.repos.filter((r) => this.repoRole(r) !== "none");
  }

  // ---- mutations ----
  log(action: AuditAction, org: string, extra: Partial<AuditEntry> = {}) {
    this.audit.unshift({
      id: this.nextId(), action, org, scope: extra.repo ? "repo" : "org",
      actor: this.user.login, created_at: this.iso(), ...extra,
    });
  }

  buildSchedule(
    input: ScheduleInput, scope: "org" | "repo", org: string, repo?: string, prev?: Schedule,
  ): MockSchedule {
    return {
      ...input, enabled: input.enabled ?? true, id: prev?.id ?? this.nextId(), scope, org,
      ...(repo ? { repo } : {}), created_by: prev?.created_by ?? this.user.login,
      created_at: prev?.created_at ?? this.iso(), updated_at: this.iso(),
    };
  }

  /** Validation mirroring the spec; returns per-field messages. */
  validateSchedule(i: ScheduleInput): Record<string, string> {
    const f: Record<string, string> = {};
    if (i.kind === "one_off") {
      if (!i.starts_at) f.starts_at = "required";
      if (!i.ends_at) f.ends_at = "required";
      if (i.starts_at && i.ends_at && Date.parse(i.starts_at) >= Date.parse(i.ends_at)) {
        f.ends_at = "must be after starts_at";
      }
    } else {
      if (!i.weekdays?.length) f.weekdays = "pick at least one day";
      if (!i.start_time) f.start_time = "required";
      if (!i.end_time) f.end_time = "required";
    }
    return f;
  }

  seed() {
    const t = this.now();
    this.orgs = [
      { installation_id: 1, login: "acme", type: "Organization", role: "owner" },
      { installation_id: 2, login: "globex", type: "Organization", role: "member" },
      { installation_id: 3, login: "octocat", type: "User", role: "owner" },
    ];
    const mk = (owner: string, name: string, open: number, role: Role = "member", extra: Partial<MockRepo> = {}): MockRepo => ({
      id: this.nextId(), owner, name, private: false, archived: false, open_prs: open,
      public_status: false, role, ...extra,
    });
    this.repos = [
      mk("acme", "web", 7), mk("acme", "api", 12, "member", { private: true }),
      mk("acme", "mobile", 3), mk("acme", "infra", 5, "member", { private: true }),
      mk("acme", "docs", 1, "member", { public_status: true }),
      mk("acme", "legacy-billing", 0, "member", { archived: true }),
      mk("globex", "reactor", 9, "maintainer"), mk("globex", "dashboard", 4, "member"),
      mk("globex", "secrets", 2, "none"),
      mk("octocat", "dotfiles", 0), mk("octocat", "hello-world", 2),
    ];
    ["search", "billing", "auth", "gateway", "emails", "analytics", "ml-pipeline", "ci-tools", "design-system", "cli"]
      .forEach((n, i) => this.repos.push(mk("acme", n, (i * 3) % 7)));

    this.repos.find((r) => r.name === "api")!.freeze = {
      id: this.nextId(), reason: "Incident INC-2041: payment retries", by: "hubot", since: this.iso(t - 3 * HOUR),
    };
    this.repos.find((r) => r.name === "reactor")!.freeze = {
      id: this.nextId(), reason: "Release cut 4.2", by: "octocat", since: this.iso(t - DAY),
    };
    const oneOff = (name: string, start: number, end: number, repo?: string) =>
      this.schedules.push(this.buildSchedule({
        kind: "one_off", name, reason: "Planned freeze", timezone: "UTC", enabled: true,
        starts_at: this.iso(start), ends_at: this.iso(end),
      }, repo ? "repo" : "org", "acme", repo));
    oneOff("Mobile release train", t - HOUR, t + 5 * HOUR, "acme/mobile");
    oneOff("Black Friday code freeze", t + 20 * DAY, t + 24 * DAY);
    this.schedules.push(this.buildSchedule({
      kind: "recurring", name: "Weekend freeze", reason: "Nobody is on call", timezone: "Europe/Dublin", enabled: true,
      weekdays: ["fri"], start_time: "17:00", end_time: "09:00",
    }, "org", "acme"));
    this.schedules.push(this.buildSchedule({
      kind: "recurring", name: "Nightly maintenance", timezone: "America/New_York",
      weekdays: ["mon", "tue", "wed", "thu"], start_time: "01:00", end_time: "03:00", enabled: false,
    }, "repo", "acme", "acme/infra"));
    this.bypasses.push({
      id: this.nextId(), repo: "acme/api", pr_number: 1873, reason: "Hotfix for the payment retry incident",
      created_by: "octocat", created_at: this.iso(t - 2 * HOUR),
    });
    const hist: [AuditAction, string, Partial<AuditEntry>, number][] = [
      ["bypass.end", "acme", { repo: "acme/web", pr_number: 412, actor: undefined }, 2 * DAY + HOUR],
      ["thaw", "acme", { repo: "acme/web", actor: "monalisa" }, 2 * DAY],
      ["freeze", "globex", { repo: "globex/reactor", reason: "Release cut 4.2" }, DAY],
      ["schedule.create", "acme", { reason: "Nobody is on call", details: { name: "Weekend freeze" } }, 6 * DAY],
      ["freeze", "acme", { repo: "acme/api", reason: "Incident INC-2041: payment retries", actor: "hubot" }, 3 * HOUR],
      ["bypass.create", "acme", { repo: "acme/api", pr_number: 1873, reason: "Hotfix for the payment retry incident" }, 2 * HOUR],
    ];
    hist.sort((a, b) => b[3] - a[3]);
    for (const [action, org, extra, ago] of hist) {
      this.audit.unshift({
        id: this.nextId(), action, org, scope: extra.repo ? "repo" : "org",
        actor: "octocat", created_at: this.iso(t - ago), ...extra,
      });
    }
    return this;
  }
}
