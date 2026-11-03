import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev the Go server runs on :8080; proxy API calls so the app stays
// same-origin, as in production.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": "http://localhost:8080" } },
  build: { outDir: "dist" },
});
