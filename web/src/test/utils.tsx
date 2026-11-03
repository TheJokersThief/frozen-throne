import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { RequestHandler } from "msw";
import { setupServer } from "msw/node";
import { MemoryRouter } from "react-router-dom";
import { App } from "../App";
import { createQueryClient } from "../api/queryClient";
import { ToastProvider } from "../components/Toast";
import { MockDb } from "../mocks/db";
import { createHandlers } from "../mocks/handlers";

let active: { close(): void } | undefined;
export function closeServer() {
  active?.close();
  active = undefined;
}

/** Renders the whole app at `path` against a fresh, seeded mock backend. */
export function renderApp(path = "/", opts: { loggedIn?: boolean; db?: MockDb; handlers?: RequestHandler[] } = {}) {
  const db = opts.db ?? new MockDb().seed();
  db.loggedIn = opts.loggedIn ?? true;
  const server = setupServer(...(opts.handlers ?? []), ...createHandlers(db));
  closeServer();
  server.listen({ onUnhandledFrame: "error" });
  active = server;
  const client = createQueryClient({ retry: false, staleTime: 0, refetchOnWindowFocus: false });
  const user = userEvent.setup();
  const utils = render(
    <QueryClientProvider client={client}>
      <ToastProvider>
        <MemoryRouter initialEntries={[path]}>
          <App />
        </MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { db, user, server, ...utils };
}
