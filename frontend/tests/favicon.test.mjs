// Guards the browser tab icon (frontend/favicon.svg). It is loaded as an image,
// so two silent failure modes are worth a test:
//   1. XML comments must not contain "--" (illegal in XML). A stray "--" makes
//      the whole SVG unparseable and the browser shows a broken image instead
//      of the icon — no build step catches it.
//   2. The mark must stay the same shape as the in-app one, so the tab icon and
//      the UI never drift apart.
// Run via tests/run.sh.

import { readFileSync } from "node:fs";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};

const favicon = readFileSync(new URL("../favicon.svg", import.meta.url), "utf8");
const logo = readFileSync(new URL("../src/components/ui/logo.tsx", import.meta.url), "utf8");

// 1. no "--" inside an XML comment (illegal in XML, breaks the whole file)
const comments = favicon.match(/<!--[\s\S]*?-->/g) ?? [];
check("has XML comments", comments.length > 0);
const badComments = comments.filter((c) => c.slice(4, -3).includes("--"));
check("no '--' inside any XML comment", badComments.length === 0, JSON.stringify(badComments));

// 2. the tile + the shared mark geometry
check("viewBox is the square tile", /viewBox="0 0 320 320"/.test(favicon));
check("has the rounded tile background", /<rect[^>]*rx="120"[^>]*fill="#2563EB"/.test(favicon));

// 3. the two mark paths are shared verbatim with the in-app logo
const paths = [...favicon.matchAll(/\sd="([^"]+)"/g)].map((m) => m[1]);
check("favicon has 2 paths", paths.length === 2, String(paths.length));
for (const d of paths) {
  check(`logo.tsx shares the path ${d.slice(0, 18)}…`, logo.includes(d));
}

console.log(failures === 0 ? "\nfavicon: ALL PASS" : `\nfavicon: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
