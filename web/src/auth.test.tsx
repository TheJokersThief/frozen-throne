import { screen, waitFor, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api/client";
import { shouldRetry } from "./api/queryClient";
import { nav } from "./config";
import { renderApp } from "./test/utils";

const BASE = "*/api/v1";
const json = (status: number, code: string, message: string) => HttpResponse.json({ code, message }, { status });

afterEach(() => vi.restoreAllMocks());

describe("logged out", () => {
  it("shows Login on a deep route and keeps the intended path for the post-login redirect", async () => {
    const to = vi.spyOn(nav, "to").mockImplementation(() => {});
    const { user } = renderApp("/repos/acme/web?tab=x", { loggedIn: false });
    await user.click(await screen.findByRole("button", { name: "Sign in with GitHub" }));
    expect(to).toHaveBeenCalledWith("/api/v1/auth/login?redirect=" + encodeURIComponent("/repos/acme/web?tab=x"));
    expect(screen.queryByText(/session expired/i)).not.toBeInTheDocument();
  });
});

describe("session expiry", () => {
  it("a 401 on a mutation clears the session and shows the expired message", async () => {
    const { user, db } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    db.loggedIn = false;
    await user.click(screen.getByRole("button", { name: "Freeze acme/web" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    expect(await screen.findByText("Your session expired, sign in again")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "acme/web" })).not.toBeInTheDocument();
  });

  it("a 401 on a query sends the user to Login without retrying", async () => {
    const { user, db, server } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    let hits = 0;
    server.events.on("request:start", ({ request }) => {
      if (new URL(request.url).pathname.endsWith("/repos")) hits++;
    });
    db.loggedIn = false;
    await user.type(screen.getByRole("searchbox", { name: "Search repositories" }), "x");
    expect(await screen.findByText("Your session expired, sign in again")).toBeInTheDocument();
    expect(hits).toBeLessThanOrEqual(2); // typed one character -> one request (+ at most the deferred one)
  });

  it("signing in again after expiry shows the app and drops the message", async () => {
    vi.stubEnv("VITE_MOCK", "1");
    const { user, db } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    db.loggedIn = false;
    await user.click(screen.getByRole("button", { name: "Freeze acme/web" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    await screen.findByText("Your session expired, sign in again");
    await user.click(screen.getByRole("button", { name: "Sign in (mock API)" }));
    await screen.findByRole("link", { name: "acme/web" });
    expect(screen.queryByText("Your session expired, sign in again")).not.toBeInTheDocument();
    vi.unstubAllEnvs();
  });

  it("never retries 401, 403 or 404, but retries other failures a bounded number of times", () => {
    const e = (s: number) => new ApiError(s, "internal", "x");
    for (const s of [401, 403, 404]) expect(shouldRetry(0, e(s))).toBe(false);
    expect(shouldRetry(0, e(500))).toBe(true);
    expect(shouldRetry(0, new ApiError(0, "network", "x"))).toBe(true);
    expect(shouldRetry(2, e(500))).toBe(false);
  });
});

describe("403 on a mutation", () => {
  async function loseRole() {
    const ctx = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    ctx.db.orgs[0].role = "member";
    ctx.db.repos.find((r) => r.name === "web")!.role = "member";
    return ctx;
  }

  it("explains the denial with the server message, keeps the session and removes the stale action", async () => {
    const { user } = await loseRole();
    await user.click(screen.getByRole("button", { name: "Freeze acme/web" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    const toast = await screen.findByText(/You don't have permission to freeze acme\/web/);
    expect(toast).toHaveTextContent("insufficient permissions on acme/web");
    await waitFor(() => {
      const b = screen.queryByRole("button", { name: "Freeze acme/web" });
      expect(b === null || (b as HTMLButtonElement).disabled).toBe(true);
    });
    expect(screen.getByRole("link", { name: "acme/web" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in with GitHub" })).not.toBeInTheDocument();
  });

  it("treats a missing-CSRF 403 as a bug, not as a permission problem", async () => {
    const { user } = renderApp("/", {
      handlers: [http.post(`${BASE}/repos/:o/:r/freeze`, () => json(403, "forbidden", "missing X-Requested-With header"))],
    });
    await screen.findByRole("link", { name: "acme/web" });
    await user.click(screen.getByRole("button", { name: "Freeze acme/web" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    expect(await screen.findByText(/Request rejected/)).toBeInTheDocument();
    expect(screen.queryByText(/don't have permission/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in with GitHub" })).not.toBeInTheDocument();
  });
});

describe("not found", () => {
  it("repo page: friendly no-access view linking back to the dashboard", async () => {
    const { user } = renderApp("/repos/nobody/missing");
    expect(await screen.findByRole("heading", { name: "Repository not found" })).toBeInTheDocument();
    expect(screen.getByText(/does not exist or you do not have access/)).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Back to the dashboard" }));
    expect(await screen.findByRole("link", { name: "acme/web" })).toBeInTheDocument();
  });

  it("org page: friendly no-access view linking back to the dashboard", async () => {
    renderApp("/orgs/nowhere");
    expect(await screen.findByRole("heading", { name: "Organization not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to the dashboard" })).toHaveAttribute("href", "/");
  });
});

describe("server and network errors", () => {
  it("dashboard shows an error with Retry on 5xx and recovers", async () => {
    let failing = true;
    const { user } = renderApp("/", {
      handlers: [http.get(`${BASE}/repos`, () => (failing ? json(500, "internal", "boom") : undefined))],
    });
    expect(await screen.findByText(/Could not load repositories/)).toBeInTheDocument();
    failing = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("link", { name: "acme/web" })).toBeInTheDocument();
  });

  it("repo page shows an error with Retry on a network failure", async () => {
    let failing = true;
    const { user } = renderApp("/repos/acme/web", {
      handlers: [http.get(`${BASE}/repos/acme/web`, () => (failing ? HttpResponse.error() : undefined))],
    });
    expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/network/i);
    failing = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "acme/web" })).toBeInTheDocument();
  });

  it("org and audit pages show Retry on 5xx", async () => {
    for (const [path, url] of [["/orgs/acme", `${BASE}/orgs/acme`], ["/audit", `${BASE}/audit`]] as const) {
      const { unmount } = renderApp(path, { handlers: [http.get(url, () => json(503, "internal", "down"))] });
      expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
      unmount();
    }
  });
});

describe("sign out", () => {
  for (const status of [401, 403]) {
    it(`lands on Login even when logout answers ${status} (already signed out)`, async () => {
      const { user, server } = renderApp();
      await screen.findByRole("link", { name: "acme/web" });
      server.use(http.post(`${BASE}/auth/logout`, () => json(status, status === 401 ? "unauthorized" : "forbidden", "nope")));
      server.use(http.get(`${BASE}/auth/me`, () => json(401, "unauthorized", "login required")));
      await user.click(screen.getByRole("button", { name: "Sign out" }));
      expect(await screen.findByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
      expect(screen.queryByText(/session expired/i)).not.toBeInTheDocument();
    });
  }
});
