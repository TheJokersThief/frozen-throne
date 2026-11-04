import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { nav } from "./config";
import { renderApp } from "./test/utils";

const row = (name: string) => screen.getByRole("link", { name }).closest("li")!;

describe("dashboard", () => {
  it("lists repos with frozen ones first and shows why they are frozen", async () => {
    renderApp();
    expect(await screen.findByRole("link", { name: "acme/api" })).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").filter((li) => li.classList.contains("repo"));
    expect(items[0]).toHaveTextContent("Frozen");
    const api = row("acme/api");
    expect(within(api).getByText("Frozen")).toBeInTheDocument();
    expect(within(api).getByText("Repo freeze")).toBeInTheDocument();
    expect(within(row("acme/web")).getByText("Open")).toBeInTheDocument();
  });

  it("searches and filters by org and frozen state", async () => {
    const { user } = renderApp();
    await screen.findByRole("link", { name: "acme/api" });
    await user.type(screen.getByRole("searchbox", { name: "Search repositories" }), "reactor");
    await waitFor(() => expect(screen.queryByRole("link", { name: "acme/api" })).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: "globex/reactor" })).toBeInTheDocument();

    await user.clear(screen.getByRole("searchbox", { name: "Search repositories" }));
    await user.selectOptions(screen.getByLabelText("Organization"), "globex");
    await waitFor(() => expect(screen.queryByRole("link", { name: "acme/web" })).not.toBeInTheDocument());
    await user.selectOptions(screen.getByLabelText("Organization"), "");
    await user.click(screen.getByRole("button", { name: "Open" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "acme/api" })).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: "acme/web" })).toBeInTheDocument();
  });

  it("freezes a repo through a confirm dialog with a reason", async () => {
    const { user, db } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    await user.click(within(row("acme/web")).getByRole("button", { name: "Freeze acme/web" }));
    const dialog = screen.getByRole("dialog", { name: /Freeze acme\/web/ });
    await user.type(within(dialog).getByLabelText(/Reason/), "Release cut");
    await user.click(within(dialog).getByRole("button", { name: "Freeze" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(within(row("acme/web")).getByText("Frozen")).toBeInTheDocument());
    expect(db.audit[0]).toMatchObject({ action: "freeze", repo: "acme/web", reason: "Release cut", actor: "octocat" });
    expect(screen.getByText(/Frozen acme\/web/)).toBeInTheDocument();
  });

  it("does not freeze when the dialog is cancelled", async () => {
    const { user, db } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    await user.click(within(row("acme/web")).getByRole("button", { name: "Freeze acme/web" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(db.repos.find((r) => r.name === "web")!.freeze).toBeUndefined();
  });

  it("thaws a repo and warns when another cause keeps it frozen", async () => {
    const { user, db } = renderApp();
    db.orgs[0].freeze = { id: 1, by: "octocat", since: db.iso() };
    await screen.findByRole("link", { name: "acme/api" });
    await user.click(within(row("acme/api")).getByRole("button", { name: "Thaw acme/api" }));
    expect(within(screen.getByRole("dialog")).getByRole("note")).toHaveTextContent("Org freeze");
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Thaw" }));
    await waitFor(() => expect(db.repos.find((r) => r.name === "api")!.freeze).toBeUndefined());
    expect(await screen.findByText(/Thawed acme\/api/)).toBeInTheDocument();
    expect(within(row("acme/api")).getByText("Frozen")).toBeInTheDocument(); // still org-frozen
  });

  it("hides controls the user is not allowed to use", async () => {
    renderApp();
    await screen.findByRole("link", { name: "globex/dashboard" });
    expect(within(row("globex/dashboard")).queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "globex/secrets" })).not.toBeInTheDocument();
    const globex = screen.getByRole("link", { name: "globex" }).closest(".org-card")!;
    expect(within(globex as HTMLElement).queryByRole("button")).not.toBeInTheDocument();
  });

  it("freezes a whole org", async () => {
    const { user, db } = renderApp();
    const acme = (await screen.findByRole("link", { name: "acme" })).closest(".org-card") as HTMLElement;
    await user.click(within(acme).getByRole("button", { name: "Freeze acme" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Freeze" }));
    await waitFor(() => expect(db.orgs[0].freeze).toBeDefined());
    await waitFor(() => expect(within(row("acme/web")).getByText("Frozen")).toBeInTheDocument());
    expect(within(row("acme/web")).getByText("Org freeze")).toBeInTheDocument();
  });
});

describe("login", () => {
  it("shows the sign-in screen when the session is missing", async () => {
    renderApp("/", { loggedIn: false });
    expect(await screen.findByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
  });

  it("real mode navigates the page to the server login (redirect = window location)", async () => {
    const to = vi.spyOn(nav, "to").mockImplementation(() => {});
    const { user } = renderApp("/", { loggedIn: false });
    await user.click(await screen.findByRole("button", { name: "Sign in with GitHub" }));
    expect(to).toHaveBeenCalledWith("/api/v1/auth/login?redirect=%2F");
    to.mockRestore();
  });

  it("mock mode keeps the mock login", async () => {
    vi.stubEnv("VITE_MOCK", "1");
    const to = vi.spyOn(nav, "to").mockImplementation(() => {});
    const { user, db } = renderApp("/", { loggedIn: false });
    await user.click(await screen.findByRole("button", { name: "Sign in (mock API)" }));
    await screen.findByRole("link", { name: "acme/web" });
    expect(db.loggedIn).toBe(true);
    expect(to).not.toHaveBeenCalled();
    to.mockRestore();
    vi.unstubAllEnvs();
  });

  it("signs out", async () => {
    const { user } = renderApp();
    await screen.findByRole("link", { name: "acme/web" });
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("button", { name: "Sign in with GitHub" })).toBeInTheDocument();
  });
});
