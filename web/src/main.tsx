import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { createQueryClient } from "./api/queryClient";
import { ToastProvider } from "./components/Toast";
import "./styles.css";

async function boot() {
  // VITE_MOCK=1 serves the OpenAPI contract from an in-browser mock (MSW).
  // Keep this a literal comparison: Vite folds it so real builds drop the mock chunk.
  if (import.meta.env.VITE_MOCK === "1") {
    const { startMockApi } = await import("./mocks/browser");
    await startMockApi();
  }
  const client = createQueryClient();
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <ToastProvider>
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </ToastProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
}
void boot();
