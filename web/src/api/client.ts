import createClient from "openapi-fetch";
import type { paths } from "./schema";
import type { ApiErrorBody } from "./types";

// Typed client for api/openapi.yaml (same-origin, cookie session).
export const api = createClient<paths>({
  baseUrl: `${globalThis.location?.origin ?? ""}/api/v1`,
  credentials: "same-origin",
  headers: { "X-Requested-With": "frozen-throne" },
  // Resolve fetch lazily so test/mocking layers that patch it later are honoured.
  fetch: async (req) => {
    try {
      return await globalThis.fetch(req);
    } catch {
      throw new ApiError(0, "network", "Network error: could not reach the server");
    }
  },
});

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: ApiErrorBody["code"] | "network",
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message);
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Returns the data of an openapi-fetch result or throws an ApiError. */
export function unwrap<T>(r: Result<T>): T {
  if (r.error !== undefined || r.data === undefined) {
    if (r.response.ok) return undefined as T; // 204 No Content
    const e = (r.error ?? {}) as Partial<ApiErrorBody>;
    throw new ApiError(
      r.response.status,
      e.code ?? "internal",
      e.message ?? `Request failed (${r.response.status})`,
      e.fields,
    );
  }
  return r.data;
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong";
}
