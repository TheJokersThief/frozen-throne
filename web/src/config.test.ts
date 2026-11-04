import { apiTarget, DEFAULT_API_TARGET, proxyConfig } from "../proxy.ts";
import { isMock, loginUrl } from "./config";

describe("config", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("mock mode is opt-in", () => {
    expect(isMock()).toBe(false);
    vi.stubEnv("VITE_MOCK", "1");
    expect(isMock()).toBe(true);
    vi.stubEnv("VITE_MOCK", "0");
    expect(isMock()).toBe(false);
  });

  it("builds the login url with an encoded redirect", () => {
    expect(loginUrl("/repos/acme/web?x=1&y=2")).toBe("/api/v1/auth/login?redirect=%2Frepos%2Facme%2Fweb%3Fx%3D1%26y%3D2");
  });

  it("proxy target defaults to :8080 and honours FT_API_TARGET", () => {
    expect(apiTarget({})).toBe(DEFAULT_API_TARGET);
    expect(apiTarget({ FT_API_TARGET: " http://api:9000/ " })).toBe("http://api:9000");
    expect(proxyConfig({ FT_API_TARGET: "http://x:1" })["/api"].target).toBe("http://x:1");
  });
});
