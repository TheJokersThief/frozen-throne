/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from "vite";
import { proxyConfig } from "./proxy.ts";
import react from "@vitejs/plugin-react";

// Real API is the default: in dev the Go server (make dev-server, :8080;
// FT_API_TARGET overrides) is proxied under /api so the app stays
// same-origin, as in production. With VITE_MOCK=1 the app answers API calls
// itself (MSW service worker) so no backend is needed.
export default defineConfig(({ mode }) => ({
  plugins: [react()],
  server: { proxy: proxyConfig(loadEnv(mode, process.cwd(), "FT_")) },
  // public/ only holds the MSW service worker: ship it in mock builds only.
  publicDir: process.env.VITE_MOCK === "1" ? "public" : false,
  build: { outDir: "dist" },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
}));
