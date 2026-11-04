/** Mock API mode is opt-in: VITE_MOCK=1 (see `npm run dev:mock`). */
export function isMock(): boolean {
  return import.meta.env.VITE_MOCK === "1";
}

/** Server-side login entry point; returns to `path` (path + search) after sign-in. */
export function loginUrl(path: string): string {
  return `/api/v1/auth/login?redirect=${encodeURIComponent(path)}`;
}

/** Full-page navigation, indirected so tests can observe it. */
export const nav = {
  to(url: string) {
    window.location.assign(url);
  },
};
