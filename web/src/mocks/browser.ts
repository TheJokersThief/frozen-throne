import { setupWorker } from "msw/browser";
import { createHandlers } from "./handlers";
import { MockDb } from "./db";

/** Starts the in-browser mock API (VITE_MOCK=1). */
export async function startMockApi() {
  const db = new MockDb().seed();
  const worker = setupWorker(...createHandlers(db, { latencyMs: 250 }));
  await worker.start({ onUnhandledFrame: "bypass", quiet: true });
  return db;
}
