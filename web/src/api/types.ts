import type { components } from "./schema";

type S = components["schemas"];
export type User = S["User"];
export type RepoSummary = S["RepoSummary"];
export type RepoDetail = S["RepoDetail"];
export type Org = S["Org"];
export type FreezeState = S["FreezeState"];
export type FreezeReason = S["FreezeReason"];
export type Schedule = S["Schedule"];
export type ScheduleInput = S["ScheduleInput"];
export type Bypass = S["Bypass"];
export type AuditEntry = S["AuditEntry"];
export type AuditAction = S["AuditAction"];
export type Permissions = S["Permissions"];
export type ApiErrorBody = S["Error"];
export type Weekday = NonNullable<ScheduleInput["weekdays"]>[number];
