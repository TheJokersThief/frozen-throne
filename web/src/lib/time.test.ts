import { describe, expect, it } from "vitest";
import { isoToZonedLocal, isValidTimezone, recurringActive, zonedLocalToIso } from "./time";

describe("timezone conversion", () => {
  it("converts wall-clock time in a zone to UTC, following DST", () => {
    expect(zonedLocalToIso("2026-07-01T17:00", "Europe/Dublin")).toBe("2026-07-01T16:00:00Z"); // IST
    expect(zonedLocalToIso("2026-01-15T17:00", "Europe/Dublin")).toBe("2026-01-15T17:00:00Z"); // GMT
    expect(zonedLocalToIso("2026-01-15T09:30", "America/New_York")).toBe("2026-01-15T14:30:00Z");
  });
  it("round-trips", () => {
    const iso = zonedLocalToIso("2026-03-29T12:00", "Asia/Kolkata");
    expect(iso).toBe("2026-03-29T06:30:00Z");
    expect(isoToZonedLocal(iso, "Asia/Kolkata")).toBe("2026-03-29T12:00");
  });
  it("validates timezone names", () => {
    expect(isValidTimezone("Europe/Dublin")).toBe(true);
    expect(isValidTimezone("Mars/Olympus")).toBe(false);
  });
});

describe("recurringActive", () => {
  const fri = { weekdays: ["fri" as const], start_time: "17:00", end_time: "09:00", timezone: "UTC" };
  it("handles windows that end the next day", () => {
    expect(recurringActive(fri, Date.parse("2026-11-06T18:00:00Z"))).toBe(true); // Fri evening
    expect(recurringActive(fri, Date.parse("2026-11-07T08:00:00Z"))).toBe(true); // Sat morning
    expect(recurringActive(fri, Date.parse("2026-11-07T10:00:00Z"))).toBe(false);
    expect(recurringActive(fri, Date.parse("2026-11-06T08:00:00Z"))).toBe(false);
  });
});
