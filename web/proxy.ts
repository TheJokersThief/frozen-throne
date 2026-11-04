/** Where the dev server proxies /api to (FT_API_TARGET overrides). */
export const DEFAULT_API_TARGET = "http://localhost:8080";

export function apiTarget(env: Record<string, string | undefined> = {}): string {
  const t = env.FT_API_TARGET?.trim();
  return t ? t.replace(/\/+$/, "") : DEFAULT_API_TARGET;
}

export function proxyConfig(env: Record<string, string | undefined> = {}) {
  return { "/api": { target: apiTarget(env), changeOrigin: false } };
}
