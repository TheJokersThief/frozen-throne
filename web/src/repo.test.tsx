import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { buildScheduleInput } from "./components/ScheduleForm";
import { renderApp } from "./test/utils";

describe("repo page: bypass", () => {
  it("requires a reason, then bypasses one PR and records it", async () => {
    const { user, db } = renderApp("/repos/acme/api");
    await screen.findByRole("heading", { name: "acme/api" });
    expect(await screen.findByText("PR #1873")).toBeInTheDocument();

    await user.type(screen.getByLabelText("PR number"), "42");
    await user.click(screen.getByRole("button", { name: "Bypass PR" }));
    expect(await screen.findByText("A reason is required")).toBeInTheDocument();
    expect(db.bypasses.some((b) => b.pr_number === 42)).toBe(false);

    await user.type(screen.getByLabelText("Reason"), "Customer-facing hotfix");
    await user.click(screen.getByRole("button", { name: "Bypass PR" }));
    expect(await screen.findByText("PR #42")).toBeInTheDocument();
    expect(db.audit[0]).toMatchObject({ action: "bypass.create", pr_number: 42, reason: "Customer-facing hotfix" });
  });

  it("shows a conflict when the PR is already bypassed, and can revoke", async () => {
    const { user, db } = renderApp("/repos/acme/api");
    await screen.findByText("PR #1873");
    await user.type(screen.getByLabelText("PR number"), "1873");
    await user.type(screen.getByLabelText("Reason"), "again");
    await user.click(screen.getByRole("button", { name: "Bypass PR" }));
    expect(await screen.findByText(/already has an active bypass/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Revoke bypass for PR #1873" }));
    await waitFor(() => expect(screen.queryByText("PR #1873")).not.toBeInTheDocument());
    expect(db.bypasses[0].end_reason).toBe("revoked");
  });

  it("clears bypasses when the repo is thawed", async () => {
    const { user, db } = renderApp("/repos/acme/api");
    await screen.findByText("PR #1873");
    await user.click(screen.getByRole("button", { name: "Thaw acme/api" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Thaw" }));
    await waitFor(() => expect(screen.getByText("No active bypasses.")).toBeInTheDocument());
    expect(db.bypasses[0].end_reason).toBe("thawed");
  });

  it("is read-only for members", async () => {
    renderApp("/repos/globex/dashboard");
    await screen.findByRole("heading", { name: "globex/dashboard" });
    expect(screen.getByText(/need maintainer access to bypass/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Bypass PR" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New window" })).not.toBeInTheDocument();
  });

  it("explains each cause of a freeze", async () => {
    renderApp("/repos/acme/mobile");
    expect(await screen.findByRole("heading", { name: "Why it is frozen" })).toBeInTheDocument();
    expect(screen.getAllByText(/Mobile release train/).length).toBeGreaterThan(0);
    expect(screen.getByText(/thaws automatically/)).toBeInTheDocument();
  });
});

describe("schedules", () => {
  it("creates a recurring window with a timezone", async () => {
    const { user, db } = renderApp("/repos/acme/web");
    await screen.findByRole("heading", { name: "acme/web" });
    await user.click(screen.getByRole("button", { name: "New window" }));
    const dialog = screen.getByRole("dialog", { name: "New freeze window" });
    await user.type(within(dialog).getByLabelText("Name"), "Quiet hours");
    await user.click(within(dialog).getByRole("radio", { name: "Recurring" }));
    const tz = within(dialog).getByLabelText("Timezone");
    await user.clear(tz);
    await user.type(tz, "Europe/Dublin");
    await user.click(within(dialog).getByRole("button", { name: "Create window" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const created = db.schedules.find((s) => s.name === "Quiet hours")!;
    expect(created).toMatchObject({ kind: "recurring", timezone: "Europe/Dublin", weekdays: ["fri"], scope: "repo", repo: "acme/web" });
    await waitFor(() => expect(screen.getAllByText(/Fri 17:00–09:00 \(Europe\/Dublin\)/)).toHaveLength(2)); // + the org-wide one
  });

  it("validates a one-off window before sending", async () => {
    const { user, db } = renderApp("/repos/acme/web");
    await screen.findByRole("heading", { name: "acme/web" });
    await user.click(screen.getByRole("button", { name: "New window" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Create window" }));
    expect(await within(dialog).findByText("Choose a start")).toBeInTheDocument();

    fireEvent.change(within(dialog).getByLabelText("Starts"), { target: { value: "2026-12-24T18:00" } });
    fireEvent.change(within(dialog).getByLabelText(/Ends/), { target: { value: "2026-12-24T09:00" } });
    await user.click(within(dialog).getByRole("button", { name: "Create window" }));
    expect(await within(dialog).findByText("The end must be after the start")).toBeInTheDocument();

    fireEvent.change(within(dialog).getByLabelText(/Ends/), { target: { value: "2026-12-27T09:00" } });
    await user.click(within(dialog).getByRole("button", { name: "Create window" }));
    await waitFor(() => expect(db.schedules.some((s) => s.starts_at && s.repo === "acme/web")).toBe(true));
  });

  it("converts one-off times from the chosen timezone to UTC", () => {
    const r = buildScheduleInput({
      name: "", reason: "", kind: "one_off", timezone: "America/New_York", enabled: true,
      start: "2026-12-24T18:00", end: "2026-12-26T09:00", weekdays: [], startTime: "", endTime: "", validFrom: "", validUntil: "",
    });
    expect(r.input).toMatchObject({ starts_at: "2026-12-24T23:00:00Z", ends_at: "2026-12-26T14:00:00Z" });
  });

  it("edits and deletes a window on the org page", async () => {
    const { user, db } = renderApp("/orgs/acme");
    await screen.findByRole("heading", { name: "acme" });
    await user.click(await screen.findByRole("button", { name: "Edit Weekend freeze" }));
    const name = within(screen.getByRole("dialog")).getByLabelText("Name");
    await user.clear(name);
    await user.type(name, "Weekend lock");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Save window" }));
    expect(await screen.findByText("Weekend lock")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Delete Weekend lock" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete window" }));
    await waitFor(() => expect(screen.queryByText("Weekend lock")).not.toBeInTheDocument());
    expect(db.audit.map((a) => a.action).slice(0, 2)).toEqual(["schedule.delete", "schedule.update"]);
  });

  it("surfaces server-side validation errors", async () => {
    const { user, db } = renderApp("/orgs/acme");
    await screen.findByRole("heading", { name: "acme" });
    // Bypass the client check by making the server reject: org owner posts a bad window.
    db.validateSchedule = () => ({ ends_at: "server says no" });
    await user.click(screen.getByRole("button", { name: "New window" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Starts"), { target: { value: "2026-12-24T09:00" } });
    fireEvent.change(within(dialog).getByLabelText(/Ends/), { target: { value: "2026-12-25T09:00" } });
    await user.click(within(dialog).getByRole("button", { name: "Create window" }));
    expect(await within(dialog).findByText("server says no")).toBeInTheDocument();
  });
});

describe("org page", () => {
  it("freezes and thaws the whole org", async () => {
    const { user, db } = renderApp("/orgs/acme");
    await user.click(await screen.findByRole("button", { name: "Freeze acme" }));
    await user.type(within(screen.getByRole("dialog")).getByLabelText(/Reason/), "Holiday");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    expect(await screen.findByRole("button", { name: "Thaw acme" })).toBeInTheDocument();
    expect(db.orgs[0].freeze?.reason).toBe("Holiday");
    await user.click(screen.getByRole("button", { name: "Thaw acme" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Thaw" }));
    expect(await screen.findByRole("button", { name: "Freeze acme" })).toBeInTheDocument();
  });
});
