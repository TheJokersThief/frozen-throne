// Captures README screenshots of the real dashboard running in mock-API mode.
// Usage: npm run screenshots   (or `just screenshots` from the repo root)
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromium } from "playwright";
import sharp from "sharp";

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outDir = path.resolve(webDir, "../docs/images");
const port = 5199;
const base = `http://localhost:${port}`;

const shots = [
  { file: "dashboard.png", url: "/", ready: "text=acme/api", height: 900 },
  { file: "repo-detail.png", url: "/repos/acme/api", ready: "text=Hotfix for the payment retry incident", height: 1000 },
];

const vite = spawn("npx", ["vite", "--port", String(port), "--strictPort"], {
  cwd: webDir, env: { ...process.env, VITE_MOCK: "1" }, stdio: "ignore",
});
try {
  for (let i = 0; ; i++) {
    try { if ((await fetch(base)).ok) break; } catch { /* not up yet */ }
    if (i > 100) throw new Error("vite did not start");
    await new Promise((r) => setTimeout(r, 200));
  }
  await mkdir(outDir, { recursive: true });
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1, colorScheme: "light" });
  for (const s of shots) {
    const page = await ctx.newPage();
    await page.setViewportSize({ width: 1280, height: s.height });
    await page.goto(base + s.url);
    await page.waitForSelector(s.ready, { timeout: 15000 });
    await page.waitForTimeout(500);
    const raw = await page.screenshot();
    const png = await sharp(raw).png({ palette: true, quality: 90, compressionLevel: 9, effort: 10 }).toBuffer();
    await writeFile(path.join(outDir, s.file), png);
    console.log(`${s.file}: ${(png.length / 1024).toFixed(0)} KiB`);
    await page.close();
  }
  await browser.close();
} finally {
  vite.kill();
}
