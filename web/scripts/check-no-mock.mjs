// Fails the build if mock code (MSW, the mock db, the service worker) leaked
// into the production bundle.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const bad = [/mockServiceWorker/i, /setupWorker/, /\bmsw\b/i, /MockDb/];
const hits = [];
(function walk(dir) {
  for (const f of readdirSync(dir)) {
    const p = join(dir, f);
    if (statSync(p).isDirectory()) walk(p);
    else if (/mockServiceWorker/i.test(f)) hits.push(`${p} (file)`);
    else for (const re of bad) if (re.test(readFileSync(p, "utf8"))) hits.push(`${p} matches ${re}`);
  }
})("dist");
if (hits.length) {
  console.error("Mock code found in production bundle:\n" + hits.join("\n"));
  process.exit(1);
}
console.log("check-no-mock: dist is free of mock code");
