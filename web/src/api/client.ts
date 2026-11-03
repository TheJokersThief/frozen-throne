import createClient from "openapi-fetch";
import type { paths } from "./schema";

// Typed client for api/openapi.yaml (same-origin, cookie session).
export const api = createClient<paths>({
  baseUrl: "/api/v1",
  headers: { "X-Requested-With": "frozen-throne" },
});
