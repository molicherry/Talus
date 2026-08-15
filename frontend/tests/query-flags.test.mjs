// Tests the ACTUAL extracted render-flag logic (computeQueryFlags in
// src/lib/query.ts). Guards the isLoading first-render-flash bug: a fresh
// (idle, no-data) ENABLED query must report isLoading=true on first render
// (matching react-query), while a DISABLED idle query reports false.
// Run via tests/run.sh.

import { getEntry, computeQueryFlags } from "./.compiled-query.mjs";

let failures = 0;
const check = (name, cond, detail = "") => {
  if (!cond) { failures++; console.log(`  FAIL: ${name} ${detail}`); }
  else console.log(`  ok: ${name}`);
};

// 1. fresh enabled query, first render (idle, no data) → isLoading true
{
  const e = getEntry('["servers"]');
  const f = computeQueryFlags(e, true);
  check("fresh enabled: isLoading=true on first render", f.isLoading === true, JSON.stringify(f));
  check("fresh enabled: isFetching=false", f.isFetching === false);
  check("fresh enabled: isError=false", f.isError === false);
}

// 2. fresh disabled query (idle, no data) → isLoading false
{
  const e = getEntry('["servers",0]');
  const f = computeQueryFlags(e, false);
  check("fresh disabled: isLoading=false", f.isLoading === false, JSON.stringify(f));
}

// 3. loading with no data → isLoading true + isFetching true
{
  const e = getEntry('["k-loading"]');
  e.status = "loading";
  const f = computeQueryFlags(e, true);
  check("loading no data: isLoading + isFetching", f.isLoading && f.isFetching, JSON.stringify(f));
}

// 4. loading with existing data (refetch) → isRefetching, not isLoading
{
  const e = getEntry('["k-refetch"]');
  e.status = "loading";
  e.data = { x: 1 };
  const f = computeQueryFlags(e, true);
  check("refetch: isRefetching=true, isLoading=false", f.isRefetching === true && f.isLoading === false, JSON.stringify(f));
}

// 5. success with data → all false
{
  const e = getEntry('["k-success"]');
  e.status = "success";
  e.data = { x: 1 };
  const f = computeQueryFlags(e, true);
  check("success: isLoading/isFetching/isRefetching/isError all false", !f.isLoading && !f.isFetching && !f.isRefetching && !f.isError, JSON.stringify(f));
}

// 6. error state → isError true, isLoading false
{
  const e = getEntry('["k-error"]');
  e.status = "error";
  e.error = new Error("boom");
  const f = computeQueryFlags(e, true);
  check("error: isError=true, isLoading=false", f.isError && !f.isLoading, JSON.stringify(f));
}

console.log(failures === 0 ? "\nquery-flags: ALL PASS" : `\nquery-flags: ${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
