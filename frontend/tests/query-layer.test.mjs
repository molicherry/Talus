// Tests the ACTUAL compiled cache layer (src/lib/query.ts) — the
// react-query replacement that guards 3 real bugs found this session:
//   1. invalidateQueries must match queryKey ARRAYS element-wise, not by
//      JSON-string startsWith (["servers"] must match ["servers", 1]).
//   2. invalidateQueries must mark INACTIVE (0-listener) entries stale so
//      they refetch on next mount (delete/edit-then-navigate-back flow).
//   3. concurrent fetches for the same key must dedup to ONE network call.
// Run via tests/run.sh (which compiles query.ts first).

import { getEntry, runQuery, invalidateQueries } from "./.compiled-query.mjs";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ── 1. dedup: two concurrent fetches → fn runs once ──
{
  let calls = 0;
  const fn = async () => { calls++; await sleep(20); return "data"; };
  const e = getEntry("k1");
  await Promise.all([runQuery(e, fn), runQuery(e, fn)]);
  check("concurrent dedup (1 fetch)", calls === 1, `calls=${calls}`);
  check("data stored", e.data === "data");
  check("status success", e.status === "success");
}

// ── 2. retry: fails once then succeeds (retriesLeft default 1) ──
{
  let calls = 0;
  const fn = async () => { calls++; if (calls < 2) throw new Error("boom"); return "ok"; };
  const e = getEntry("k2");
  await runQuery(e, fn);
  check("retry then success", e.status === "success" && e.data === "ok", `status=${e.status}`);
  check("retried once (2 calls)", calls === 2, `calls=${calls}`);
}

// ── 3. error surfaces after retries exhausted ──
{
  let calls = 0;
  const fn = async () => { calls++; throw new Error("always"); };
  const e = getEntry("k3");
  await runQuery(e, fn);
  check("error after retry", e.status === "error" && e.error instanceof Error, `status=${e.status}`);
  check("2 attempts", calls === 2, `calls=${calls}`);
}

// ── 4. prefix match: element-wise, so ["servers"] matches ["servers", 1] ──
{
  let listCalls = 0, detailCalls = 0;
  const listFn = async () => { listCalls++; return ["a"]; };
  const detailFn = async () => { detailCalls++; return { id: 1 }; };
  const el = getEntry('["servers"]'); el.queryKey = ["servers"]; el.lastQueryFn = listFn; el.lastStaleTime = 0;
  const ed = getEntry('["servers",1]'); ed.queryKey = ["servers", 1]; ed.lastQueryFn = detailFn; ed.lastStaleTime = 0;
  const other = getEntry('["metrics",1,"1h"]'); other.queryKey = ["metrics", 1, "1h"]; other.lastQueryFn = async () => 1; other.lastStaleTime = 0;
  const ls = new Set([() => {}]);
  el.listeners = ls; ed.listeners = ls; other.listeners = new Set();
  await runQuery(el, listFn); await runQuery(ed, detailFn); await runQuery(other, async () => 1);
  listCalls = 0; detailCalls = 0;
  invalidateQueries(["servers"]);
  await sleep(30);
  check("list refetched (active)", listCalls === 1, `calls=${listCalls}`);
  check("detail refetched (prefix match by ARRAY, the fixed bug)", detailCalls === 1, `calls=${detailCalls}`);
  check("non-matching key untouched", other.status === "success" && other.lastFetched !== 0);
}

// ── 5. inactive entries marked stale (delete-from-detail flow) ──
{
  const listKey = '["servers"]';
  const listFn = async () => ({ servers: ["a", "b"] });
  const e = getEntry(listKey);
  e.queryKey = ["servers"];
  e.lastQueryFn = listFn;
  e.lastStaleTime = 30000;
  await runQuery(e, listFn);
  check("initial fetch ok", e.status === "success");
  e.listeners.clear(); // component unmounted
  invalidateQueries(["servers"]);
  check("inactive entry marked stale", e.lastFetched === 0, `lastFetched=${e.lastFetched}`);
  const isStale = !(e.status === "success" && Date.now() - e.lastFetched < 30000);
  check("remount would refetch", isStale === true);
}

// ── 6. listeners notified on state change ──
{
  let notified = 0;
  const e = getEntry("k6");
  e.listeners.add(() => notified++);
  await runQuery(e, async () => "v");
  check("listener notified (loading + success)", notified >= 2, `notified=${notified}`);
}

console.log(failures === 0 ? "\nquery-layer: ALL PASS" : `\nquery-layer: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
