import assert from "node:assert/strict";
import {
  getEntry, runQuery, cancelQuery, subscribeQuery, removeQueries, pruneQueryCache,
} from "./.compiled-query.mjs";

const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const tick = () => Promise.resolve();

// Last observer owns cancellation; another observer can still finish the query.
{
  const entry = getEntry("shared-observers");
  const work = deferred();
  const leaveA = subscribeQuery(entry, () => {});
  const leaveB = subscribeQuery(entry, () => {});
  let signal;
  const request = runQuery(entry, async (context) => { signal = context.signal; return work.promise; });
  await tick();
  leaveA();
  assert.equal(signal.aborted, false);
  work.resolve("both");
  await request;
  assert.equal(entry.data, "both");
  leaveB();
}

// A transport that ignores abort must never overwrite a replacement request.
{
  const entry = getEntry("late-result");
  const old = deferred();
  const leave = subscribeQuery(entry, () => {});
  let oldSignal;
  const oldRequest = runQuery(entry, ({ signal }) => { oldSignal = signal; return old.promise; });
  await tick();
  leave();
  assert.equal(oldSignal.aborted, true);
  assert.equal(entry.status, "idle");
  assert.equal(entry.inFlight, null);
  const leaveAgain = subscribeQuery(entry, () => {});
  const next = deferred();
  const nextRequest = runQuery(entry, () => next.promise);
  await tick();
  old.resolve("old-account");
  await oldRequest;
  assert.equal(entry.status, "loading");
  assert.notEqual(entry.inFlight, null);
  next.resolve("new-account");
  await nextRequest;
  assert.equal(entry.data, "new-account");
  leaveAgain();
}

// An independent AbortError is neither retried nor rendered as an API failure.
{
  const entry = getEntry("abort-error");
  let calls = 0;
  await runQuery(entry, async () => { calls++; throw new DOMException("aborted", "AbortError"); });
  assert.equal(calls, 1);
  assert.equal(entry.status, "idle");
  assert.equal(entry.error, null);
  await runQuery(entry, async () => "cached");
  const work = deferred();
  const request = runQuery(entry, () => work.promise);
  await tick();
  cancelQuery(entry);
  assert.equal(entry.data, "cached");
  assert.equal(entry.status, "success");
  work.reject(new Error("late failure"));
  await request;
  assert.equal(entry.error, null);
  assert.equal(entry.data, "cached");
}

// Removal erases data and prevents pending work from repopulating the old entry.
{
  const entry = getEntry('["usage-log",1,"123"]');
  entry.queryKey = ["usage-log", 1, "123"];
  await runQuery(entry, async () => "private");
  const work = deferred();
  const request = runQuery(entry, () => work.promise);
  await tick();
  removeQueries(["usage-log"], (key) => key[1] === 1);
  assert.equal(entry.data, undefined);
  work.resolve("private-late");
  await request;
  assert.equal(entry.data, undefined);
  assert.notEqual(getEntry(entry.key), entry);
}

// LRU respects active/pinned pages and separately expires idle details.
{
  const entries = [0, 1, 2, 3].map((page) => {
    const entry = getEntry(JSON.stringify(["bounded-logs", page]));
    entry.queryKey = ["bounded-logs", page];
    entry.lastUsed = page + 1;
    return entry;
  });
  const leave = subscribeQuery(entries[3], () => {});
  pruneQueryCache({ prefix: ["bounded-logs"], maxEntries: 2, retain: (key) => key[1] === 0 });
  assert.equal(getEntry(entries[0].key), entries[0]);
  assert.equal(getEntry(entries[3].key), entries[3]);
  assert.notEqual(getEntry(entries[1].key), entries[1]);
  assert.notEqual(getEntry(entries[2].key), entries[2]);
  leave();
  removeQueries(["bounded-logs"]);
}

console.log("query-cancellation: ALL PASS");
