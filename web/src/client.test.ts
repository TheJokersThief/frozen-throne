import { api } from "./api/client";

describe("api client", () => {
  it("sends same-origin credentials and the CSRF header", async () => {
    const seen: Request[] = [];
    const orig = globalThis.fetch;
    globalThis.fetch = (async (req: Request) => {
      seen.push(req);
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    }) as typeof fetch;
    try {
      await api.GET("/auth/me");
    } finally {
      globalThis.fetch = orig;
    }
    expect(seen[0].credentials).toBe("same-origin");
    expect(seen[0].headers.get("X-Requested-With")).toBe("frozen-throne");
  });
});
