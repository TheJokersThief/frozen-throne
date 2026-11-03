import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { ApiError } from "./client";
import { apiEvents, session } from "./session";

/** Auth/permission/visibility failures are answers, not glitches: never retry them. */
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && [401, 403, 404].includes(error.status)) return false;
  return failureCount < 2;
}

/** Queries whose payload carries the caller's permissions. */
const PERMISSION_KEYS = ["repos", "repo", "orgs", "org", "org-schedules"];

export interface MutationMeta extends Record<string, unknown> {
  /** Human description of the attempted action, e.g. "freeze acme/web". */
  action?: (variables: never) => string;
}

export function createQueryClient(opts: { retry?: boolean; staleTime?: number; refetchOnWindowFocus?: boolean } = {}): QueryClient {
  const expire = () => {
    if (session.isExpired()) return;
    session.setExpired(true);
    void client.resetQueries();
  };
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({
      onError(error, query) {
        if (!(error instanceof ApiError) || error.status !== 401) return;
        // The initial "who am I" 401 just means logged out; only a later 401 is an expiry.
        if (query.queryKey[0] === "me" && query.state.data === undefined) return;
        expire();
      },
    }),
    mutationCache: new MutationCache({
      onError(error, variables, _ctx, mutation) {
        if (!(error instanceof ApiError)) return;
        if (error.status === 401) {
          if (mutation.options.mutationKey?.[0] !== "logout") expire();
        } else if (error.status === 403 && mutation.options.mutationKey?.[0] !== "logout") {
          if (/X-Requested-With/i.test(error.message)) {
            apiEvents.emit({ type: "rejected", message: error.message });
            return;
          }
          const describe = (mutation.meta as MutationMeta | undefined)?.action as ((v: unknown) => string) | undefined;
          apiEvents.emit({ type: "forbidden", action: describe?.(variables) ?? "do that", message: error.message });
          for (const k of PERMISSION_KEYS) void client.invalidateQueries({ queryKey: [k] });
        }
      },
    }),
    defaultOptions: {
      queries: {
        retry: opts.retry === false ? false : shouldRetry,
        staleTime: opts.staleTime ?? 5_000,
        refetchOnWindowFocus: opts.refetchOnWindowFocus ?? true,
      },
    },
  });
  return client;
}
