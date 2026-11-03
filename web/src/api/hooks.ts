import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, unwrap } from "./client";
import type { AuditAction, ScheduleInput } from "./types";

export const keys = {
  me: ["me"] as const,
  repos: ["repos"] as const,
  repo: (o: string, r: string) => ["repo", o, r] as const,
  orgs: ["orgs"] as const,
  org: (o: string) => ["org", o] as const,
  orgSchedules: (o: string) => ["org-schedules", o] as const,
  audit: ["audit"] as const,
};

export function useMe() {
  return useQuery({
    queryKey: keys.me,
    queryFn: async () => unwrap(await api.GET("/auth/me")),
    retry: false,
    staleTime: 60_000,
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/auth/logout")),
    onSuccess: () => qc.clear(),
  });
}

export interface RepoFilters {
  q: string;
  org: string;
  state: "" | "frozen" | "thawed";
  offset: number;
  limit: number;
}

export function useRepos(f: RepoFilters) {
  return useQuery({
    queryKey: [...keys.repos, f],
    queryFn: async () =>
      unwrap(
        await api.GET("/repos", {
          params: {
            query: {
              q: f.q || undefined,
              org: f.org || undefined,
              state: f.state || undefined,
              limit: f.limit,
              offset: f.offset,
            },
          },
        }),
      ),
    placeholderData: (prev) => prev,
  });
}

export function useRepo(owner: string, repo: string) {
  return useQuery({
    queryKey: keys.repo(owner, repo),
    queryFn: async () =>
      unwrap(await api.GET("/repos/{owner}/{repo}", { params: { path: { owner, repo } } })),
  });
}

export function useOrgs() {
  return useQuery({
    queryKey: keys.orgs,
    queryFn: async () => unwrap(await api.GET("/orgs")),
  });
}

export function useOrg(org: string) {
  return useQuery({
    queryKey: keys.org(org),
    queryFn: async () => unwrap(await api.GET("/orgs/{org}", { params: { path: { org } } })),
  });
}

export function useOrgSchedules(org: string) {
  return useQuery({
    queryKey: keys.orgSchedules(org),
    queryFn: async () =>
      unwrap(await api.GET("/orgs/{org}/schedules", { params: { path: { org } } })),
  });
}

export interface AuditFilters {
  org: string;
  repo: string;
  actor: string;
  action: "" | AuditAction;
}

export function useAudit(f: AuditFilters) {
  return useInfiniteQuery({
    queryKey: [...keys.audit, f],
    initialPageParam: undefined as number | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET("/audit", {
          params: {
            query: {
              org: f.org || undefined,
              repo: f.repo || undefined,
              actor: f.actor || undefined,
              action: f.action || undefined,
              before: pageParam,
              limit: 30,
            },
          },
        }),
      ),
    getNextPageParam: (last) => last.next_before,
  });
}

/** Any change can alter freeze state anywhere, so refresh everything but `me`. */
function useInvalidateAll() {
  const qc = useQueryClient();
  return () => {
    for (const k of ["repos", "repo", "orgs", "org", "org-schedules", "audit"]) {
      void qc.invalidateQueries({ queryKey: [k] });
    }
  };
}

export function useFreezeRepo() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { owner: string; repo: string; reason?: string }) =>
      unwrap(
        await api.POST("/repos/{owner}/{repo}/freeze", {
          params: { path: { owner: v.owner, repo: v.repo } },
          body: { reason: v.reason || undefined },
        }),
      ),
    onSuccess: done,
  });
}

export function useThawRepo() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { owner: string; repo: string }) =>
      unwrap(await api.DELETE("/repos/{owner}/{repo}/freeze", { params: { path: v } })),
    onSuccess: done,
  });
}

export function useFreezeOrg() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { org: string; reason?: string }) =>
      unwrap(
        await api.POST("/orgs/{org}/freeze", {
          params: { path: { org: v.org } },
          body: { reason: v.reason || undefined },
        }),
      ),
    onSuccess: done,
  });
}

export function useThawOrg() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { org: string }) =>
      unwrap(await api.DELETE("/orgs/{org}/freeze", { params: { path: v } })),
    onSuccess: done,
  });
}

export function useSetPublicStatus() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { owner: string; repo: string; enabled: boolean }) =>
      unwrap(
        await api.PUT("/repos/{owner}/{repo}/public-status", {
          params: { path: { owner: v.owner, repo: v.repo } },
          body: { enabled: v.enabled },
        }),
      ),
    onSuccess: done,
  });
}

export type ScheduleTarget = { org: string } | { owner: string; repo: string };

export function useCreateSchedule() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { target: ScheduleTarget; input: ScheduleInput }) =>
      "org" in v.target
        ? unwrap(
            await api.POST("/orgs/{org}/schedules", {
              params: { path: { org: v.target.org } },
              body: v.input,
            }),
          )
        : unwrap(
            await api.POST("/repos/{owner}/{repo}/schedules", {
              params: { path: v.target },
              body: v.input,
            }),
          ),
    onSuccess: done,
  });
}

export function useUpdateSchedule() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { id: number; input: ScheduleInput }) =>
      unwrap(
        await api.PUT("/schedules/{id}", { params: { path: { id: v.id } }, body: v.input }),
      ),
    onSuccess: done,
  });
}

export function useDeleteSchedule() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { id: number }) =>
      unwrap(await api.DELETE("/schedules/{id}", { params: { path: v } })),
    onSuccess: done,
  });
}

export function useCreateBypass() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { owner: string; repo: string; number: number; reason: string }) =>
      unwrap(
        await api.POST("/repos/{owner}/{repo}/pulls/{number}/bypass", {
          params: { path: { owner: v.owner, repo: v.repo, number: v.number } },
          body: { reason: v.reason },
        }),
      ),
    onSuccess: done,
  });
}

export function useDeleteBypass() {
  const done = useInvalidateAll();
  return useMutation({
    mutationFn: async (v: { owner: string; repo: string; number: number }) =>
      unwrap(await api.DELETE("/repos/{owner}/{repo}/pulls/{number}/bypass", { params: { path: v } })),
    onSuccess: done,
  });
}
