// Tests the ACTUAL locale JSON data (src/i18n/locales/*.json) — the source of
// truth for the zero-dependency i18n shim. Guards:
//   1. en ↔ zh-CN 100% key parity (a missing zh key silently falls back to en,
//      hiding translation gaps).
//   2. No plural forms (_one/_other) — the shim only does {{var}} interpolation.
//   3. Interpolation placeholders are simple {{name}} (shim's regex).

import { readFileSync } from "node:fs";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};

const en = JSON.parse(readFileSync(new URL("./../src/i18n/locales/en.json", import.meta.url), "utf8"));
const zh = JSON.parse(readFileSync(new URL("./../src/i18n/locales/zh-CN.json", import.meta.url), "utf8"));

// Flatten nested object to dotted keys → string values.
function flatten(o, prefix = "", out = {}) {
  for (const [k, v] of Object.entries(o)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") out[key] = v;
    else if (typeof v === "object" && v !== null) flatten(v, key, out);
    else out[key] = String(v);
  }
  return out;
}

const enFlat = flatten(en);
const zhFlat = flatten(zh);

check("en has keys", Object.keys(enFlat).length > 100, `${Object.keys(enFlat).length} keys`);

// 1. key parity
const missingInZh = Object.keys(enFlat).filter((k) => !(k in zhFlat));
const extraInZh = Object.keys(zhFlat).filter((k) => !(k in enFlat));
check("zh-CN has every en key (no silent fallback)", missingInZh.length === 0, `missing=${missingInZh.slice(0, 5)}`);
check("zh-CN has no extra keys", extraInZh.length === 0, `extra=${extraInZh.slice(0, 5)}`);

// 2. no plural forms
const pluralKeys = [...Object.keys(enFlat), ...Object.keys(zhFlat)].filter((k) =>
  /_(one|other|many|few|zero)$/.test(k),
);
check("no plural-form keys", pluralKeys.length === 0, pluralKeys.slice(0, 5));

// 3. interpolation placeholders are simple {{name}}
const badPlaceholders = [];
for (const [k, v] of Object.entries(enFlat)) {
  for (const m of v.matchAll(/\{\{([^}]+)\}\}/g)) {
    if (!/^\w+$/.test(m[1])) badPlaceholders.push(`${k}: {{${m[1]}}}`);
  }
}
check("placeholders are simple {{name}}", badPlaceholders.length === 0, badPlaceholders.slice(0, 3));

// 4. every string non-empty
const emptyValues = Object.entries(enFlat).filter(([, v]) => v.trim() === "").map(([k]) => k);
check("no empty strings in en", emptyValues.length === 0, emptyValues.slice(0, 3));

console.log(failures === 0 ? "\ni18n-locale: ALL PASS" : `\ni18n-locale: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
