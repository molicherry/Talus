// Tests for src/features/monitoring/lib/series.ts (compiled by tests/run.sh).
//
// The backend aggregates with time_bucket(...) GROUP BY bucket, so buckets with
// no samples are simply absent. These tests pin the alignment that turns that
// sparse response back into a dense, time-proportional grid with `null` gaps —
// and, critically, that "last collected" never comes from a filled bucket.

import assert from "node:assert/strict";

import { buildAlignedSeries, isolatedIndices } from "./.compiled-series.mjs";

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`  ok: ${name}`);
  } catch (err) {
    failures++;
    console.log(`  FAIL: ${name}\n    ${err.message}`);
  }
}

const WINDOW = {
  from: "2026-09-20T10:00:00.000Z",
  to: "2026-09-20T10:10:00.000Z",
  intervalMs: 60_000, // 10:00 .. 10:10 inclusive = 11 buckets
};

const at = (min, extra = {}) => ({
  time: new Date(Date.UTC(2026, 8, 20, 10, min, 0)).toISOString(),
  ...extra,
});


check("empty input still yields the full window grid", () => {
  const out = buildAlignedSeries([], WINDOW, ["cpu_percent"]);
  assert.equal(out.times.length, 11);
  assert.equal(out.times[0], WINDOW.from);
  assert.equal(out.times[10], WINDOW.to);
  assert.deepEqual(out.series.cpu_percent, new Array(11).fill(null));
  assert.equal(out.lastSampleTime, null);
});

check("missing buckets become null, not zero (the reported bug)", () => {
  const out = buildAlignedSeries(
    [at(0, { cpu_percent: 10 }), at(1, { cpu_percent: 20 }), at(10, { cpu_percent: 30 })],
    WINDOW,
    ["cpu_percent"],
  );
  const v = out.series.cpu_percent;
  assert.equal(v.length, 11);
  assert.equal(v[0], 10);
  assert.equal(v[1], 20);
  for (let i = 2; i <= 9; i++) assert.equal(v[i], null, `bucket ${i} should be null`);
  assert.equal(v[10], 30);
});

check("a real zero is preserved", () => {
  const out = buildAlignedSeries([at(5, { cpu_percent: 0 })], WINDOW, ["cpu_percent"]);
  assert.equal(out.series.cpu_percent[5], 0);
  assert.notEqual(out.series.cpu_percent[5], null);
});

check("an explicit null value in a point stays null", () => {
  const out = buildAlignedSeries([at(6, { cpu_percent: null })], WINDOW, ["cpu_percent"]);
  assert.equal(out.series.cpu_percent[6], null);
  // The sample still happened, so "last collected" advances.
  assert.equal(out.lastSampleTime, at(6).time);
});

check("head and tail gaps are filled as empty buckets", () => {
  const out = buildAlignedSeries([at(4, { cpu_percent: 7 })], WINDOW, ["cpu_percent"]);
  const v = out.series.cpu_percent;
  for (let i = 0; i <= 3; i++) assert.equal(v[i], null, `head ${i}`);
  assert.equal(v[4], 7);
  for (let i = 5; i <= 10; i++) assert.equal(v[i], null, `tail ${i}`);
});

check("single point lands in its bucket", () => {
  const out = buildAlignedSeries([at(3, { cpu_percent: 5 })], WINDOW, ["cpu_percent"]);
  assert.equal(out.series.cpu_percent[3], 5);
  assert.equal(out.series.cpu_percent.filter((x) => x !== null).length, 1);
});

check("duplicate timestamps: the later value wins, one bucket", () => {
  const out = buildAlignedSeries(
    [at(2, { cpu_percent: 1 }), at(2, { cpu_percent: 2 })],
    WINDOW,
    ["cpu_percent"],
  );
  assert.equal(out.series.cpu_percent[2], 2);
  assert.equal(out.times.length, 11);
});

check("out-of-order input matches sorted input", () => {
  const unsorted = buildAlignedSeries(
    [at(5, { cpu_percent: 50 }), at(0, { cpu_percent: 10 })],
    WINDOW,
    ["cpu_percent"],
  );
  assert.equal(unsorted.series.cpu_percent[0], 10);
  assert.equal(unsorted.series.cpu_percent[5], 50);
});

check("invalid timestamps are skipped without throwing", () => {
  const out = buildAlignedSeries(
    [{ time: "not-a-date", cpu_percent: 99 }, at(1, { cpu_percent: 20 })],
    WINDOW,
    ["cpu_percent"],
  );
  assert.equal(out.series.cpu_percent[1], 20);
  assert.equal(out.series.cpu_percent.filter((x) => x !== null).length, 1);
  assert.equal(out.lastSampleTime, at(1).time);
});

check("points outside the window do not leak into the grid", () => {
  const out = buildAlignedSeries(
    [{ time: "2026-09-20T09:59:00.000Z", cpu_percent: 99 }, at(1, { cpu_percent: 20 })],
    WINDOW,
    ["cpu_percent"],
  );
  assert.equal(out.series.cpu_percent[1], 20);
  assert.equal(out.series.cpu_percent.includes(99), false);
});

check("lastSampleTime is the raw sample time, not the bucket start", () => {
  const raw = "2026-09-20T10:07:30.000Z";
  const out = buildAlignedSeries([{ time: raw, cpu_percent: 42 }], WINDOW, ["cpu_percent"]);
  // Value lands in the 10:07 bucket...
  assert.equal(out.times[7], "2026-09-20T10:07:00.000Z");
  assert.equal(out.series.cpu_percent[7], 42);
  // ...but "last collected" keeps the real instant.
  assert.equal(out.lastSampleTime, raw);
});

check("appending empty tail buckets never moves lastSampleTime", () => {
  const points = [at(0, { cpu_percent: 10 }), at(1, { cpu_percent: 20 })];
  const partial = buildAlignedSeries(points, { ...WINDOW, to: at(3).time }, ["cpu_percent"]);
  const full = buildAlignedSeries(points, WINDOW, ["cpu_percent"]);
  assert.equal(partial.lastSampleTime, at(1).time);
  assert.equal(full.lastSampleTime, at(1).time);
  assert.equal(partial.times.length, 4);
  assert.equal(full.times.length, 11);
});

check("multiple fields stay aligned to the same grid", () => {
  const out = buildAlignedSeries(
    [at(1, { cpu_percent: 1, load_1: 0.5 }), at(2, { cpu_percent: 2 })],
    WINDOW,
    ["cpu_percent", "load_1"],
  );
  assert.equal(out.series.cpu_percent[1], 1);
  assert.equal(out.series.load_1[1], 0.5);
  assert.equal(out.series.load_1[2], null, "a field absent from a sample is null");
  assert.equal(out.series.cpu_percent.length, out.times.length);
  assert.equal(out.series.load_1.length, out.times.length);
});

check("a degenerate window returns an empty grid instead of NaN", () => {
  const out = buildAlignedSeries([at(1, { cpu_percent: 1 })], { ...WINDOW, intervalMs: 0 }, [
    "cpu_percent",
  ]);
  assert.deepEqual(out.times, []);
  assert.equal(out.lastSampleTime, null);
});

check("isolatedIndices finds no isolated runs in contiguous data", () => {
  assert.deepEqual(isolatedIndices([1, 2, 3]), []);
  assert.deepEqual(isolatedIndices([]), []);
});

check("isolatedIndices returns a run of exactly one", () => {
  assert.deepEqual(isolatedIndices([1]), [0]);
  assert.deepEqual(isolatedIndices([null, 1, null]), [1]);
  assert.deepEqual(isolatedIndices([1, null, 2]), [0, 2]);
});

check("isolatedIndices keeps runs of two or more as line segments", () => {
  assert.deepEqual(isolatedIndices([1, 2]), []);
  assert.deepEqual(isolatedIndices([1, 2, null, 3]), [3]);
});

check("isolatedIndices handles leading and trailing isolated points", () => {
  assert.deepEqual(isolatedIndices([5, null, 6, 7]), [0]);
  assert.deepEqual(isolatedIndices([6, 7, null, 5]), [3]);
});

check("isolatedIndices ignores undefined and non-finite values", () => {
  assert.deepEqual(isolatedIndices([undefined, Number.NaN, 4, undefined]), [2]);
});

check("isolatedIndices on a filled grid with one gap bucket", () => {
  // A real sample at each end, nothing in between: both ends are isolated.
  assert.deepEqual(isolatedIndices([1, null, null, 2]), [0, 3]);
});

console.log(failures === 0 ? "\nmonitoring-series: ALL PASS" : `\nmonitoring-series: ${failures} FAILED`);
process.exit(failures === 0 ? 0 : 1);
