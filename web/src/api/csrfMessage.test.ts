import { describe, expect, it } from "vitest";
import { isCsrfRejection } from "./queryClient";

// Must match the Go API's literal; TestCSRFRejectionMessage in cmd/dev-server asserts it.
const GO_CSRF_MESSAGE = "missing X-Requested-With header";

describe("CSRF rejection detection", () => {
  it("recognises the exact message the Go API sends", () => {
    expect(isCsrfRejection(GO_CSRF_MESSAGE)).toBe(true);
  });
  it("does not mistake ordinary permission denials for CSRF", () => {
    expect(isCsrfRejection("forbidden")).toBe(false);
    expect(isCsrfRejection("you need maintainer access")).toBe(false);
  });
});
