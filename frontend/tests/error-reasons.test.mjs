// The backend attaches a stable `reason` to every error and expects the client
// to translate it as `errors.<reason>` (see src/lib/api-error.ts). Nothing else
// enforces that a NEW backend reason ships with translations: without this
// test a missing key silently degrades to "request failed with status N".
//
// It reads backend/internal/server/errors.go directly, so the two sides cannot
// drift apart. Run via tests/run.sh.

import { readFileSync } from "node:fs";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};

const go = readFileSync(new URL("../../backend/internal/server/errors.go", import.meta.url), "utf8");
const reasons = [...go.matchAll(/^\tReason\w+\s+=\s+"([^"]+)"/gm)].map((m) => m[1]);
check("backend declares reasons", reasons.length > 20, `found ${reasons.length}`);

// The auth middleware cannot import the server package, so it duplicates the
// unauthorized/forbidden strings. A typo there would silently stop translating
// real 401s, so the duplicates must exist in the canonical set.
const mw = readFileSync(
  new URL("../../backend/internal/server/middleware/auth.go", import.meta.url),
  "utf8",
);
const mwReasons = [...mw.matchAll(/^\treason\w+\s+=\s+"([^"]+)"/gm)].map((m) => m[1]);
check("middleware declares its reasons", mwReasons.length > 0, JSON.stringify(mwReasons));
for (const r of mwReasons) {
  check(`middleware reason "${r}" exists in errors.go`, reasons.includes(r));
}

const locales = ["en", "zh-CN"].map((loc) => [
  loc,
  JSON.parse(readFileSync(new URL(`../src/i18n/locales/${loc}.json`, import.meta.url), "utf8")),
]);

// Every reason must have a translation in every locale...
for (const [loc, dict] of locales) {
  const missing = reasons.filter((r) => typeof dict.errors?.[r] !== "string");
  check(`${loc}: every backend reason is translated`, missing.length === 0, JSON.stringify(missing));
}
// ...and no locale may carry an errors.* key the backend never sends (a typo or
// a stale reason would otherwise sit there forever).
for (const [loc, dict] of locales) {
  const extra = Object.keys(dict.errors ?? {}).filter((k) => !reasons.includes(k));
  check(`${loc}: no unknown errors.* keys`, extra.length === 0, JSON.stringify(extra));
}

// Placeholders must match between locales, or one language shows {{min}} raw.
for (const reason of reasons) {
  const ph = (s) => [...(s ?? "").matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();
  const [en, zh] = locales.map(([, d]) => ph(d.errors?.[reason]));
  check(`placeholders match for ${reason}`, JSON.stringify(en) === JSON.stringify(zh), `${JSON.stringify(en)} vs ${JSON.stringify(zh)}`);
}

console.log(failures === 0 ? "\nerror-reasons: ALL PASS" : `\nerror-reasons: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
