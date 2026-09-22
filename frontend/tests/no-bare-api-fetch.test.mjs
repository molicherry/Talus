// Every API call must go through src/lib/api-client.ts: it carries the token,
// honours VITE_API_BASE_URL (a bare relative fetch hits the frontend's own
// origin when the two are deployed separately) and reads the error envelope
// that the reason-based translations need.
//
// Three call sites had drifted (auth/password, auth/setup twice), so this test
// reads the source instead of trusting review. Run via tests/run.sh.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};

const SRC = new URL("../src", import.meta.url).pathname;
const files = [];
(function walk(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) walk(full);
    else if (/\.(ts|tsx)$/.test(full)) files.push(full);
  }
})(SRC);

const offenders = [];
for (const file of files) {
  if (file.endsWith("lib/api-client.ts")) continue; // the one place allowed to fetch
  const src = readFileSync(file, "utf8");
  for (const line of src.split("\n")) {
    // fetch("/api/...") or fetch(`/api/...`) — a relative API call.
    if (/fetch\(\s*[`"']\/api\//.test(line)) offenders.push(`${file.replace(SRC, "src")}: ${line.trim()}`);
  }
}
check("no relative fetch('/api/...') outside the api client", offenders.length === 0, JSON.stringify(offenders, null, 2));

console.log(failures === 0 ? "\nno-bare-api-fetch: ALL PASS" : `\nno-bare-api-fetch: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
