import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { MockDb } from "./mocks/db";
import { renderApp } from "./test/utils";

describe("audit log", () => {
  it("shows who did what, newest first, with reasons", async () => {
    renderApp("/audit");
    expect(await screen.findByRole("heading", { name: "Audit log" })).toBeInTheDocument();
    const items = await screen.findAllByRole("listitem");
    expect(items[0]).toHaveTextContent("Bypassed PR");
    expect(items[0]).toHaveTextContent("PR #1873");
    expect(items[0]).toHaveTextContent("Hotfix for the payment retry incident");
    expect(screen.getByText(/by hubot/)).toBeInTheDocument();
    expect(screen.getByText(/automatically/)).toBeInTheDocument(); // system action without actor
  });

  it("filters by action and org", async () => {
    const { user } = renderApp("/audit");
    await screen.findAllByText("Froze", { selector: "strong" });
    await user.selectOptions(screen.getByLabelText("Action"), "thaw");
    await waitFor(() => expect(screen.queryByText("Froze", { selector: "strong" })).not.toBeInTheDocument());
    expect(screen.getByText("Thawed", { selector: "strong" })).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Action"), "");
    await user.selectOptions(screen.getByLabelText("Organization"), "globex");
    await waitFor(() => expect(screen.queryByText(/acme\/api/)).not.toBeInTheDocument());
    expect(screen.getByText("globex/reactor")).toBeInTheDocument();
  });

  it("paginates with a cursor", async () => {
    const db = new MockDb().seed();
    for (let i = 0; i < 40; i++) db.log("thaw", "acme", { repo: `acme/r${i}` });
    const { user } = renderApp("/audit", { db });
    await screen.findByText("acme/r39");
    expect(screen.queryByText("acme/r0")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Load older entries" }));
    expect(await screen.findByText("acme/r0")).toBeInTheDocument();
  });

  it("is reachable from navigation and links a repo to its filtered log", async () => {
    const { user } = renderApp("/");
    await user.click(await screen.findByRole("link", { name: "Audit log" }));
    expect(await screen.findByRole("heading", { name: "Audit log" })).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Main" })).getByRole("link", { name: "Audit log" })).toHaveAttribute("aria-current", "page");
  });
});
