// Tests for the chart axis/statistic vocabulary and pure helpers.
//
// These pin the data-contract halves of TC-04-01 (ranges, intervals, axis units
// and zero-vs-null) and TC-04-04 (non-colour series distinction), which the
// browser suite asserts at the DOM level.

import assert from "node:assert/strict";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";

import {
  AXIS_SERIES,
  DATA_TABLE_PAGE_SIZE,
  axisUnit,
  chartDescription,
  coverageCell,
  dataTableCaption,
  pageSlice,
  alignmentFields,
  bucketCoverage,
  downsampleNotice,
  formatAxisValue,
  formatBytesPerSecond,
  formatCoverage,
  seriesForStat,
  seriesLabelKey,
  seriesPattern,
  weightedMean,
} from "./.compiled-chart-config-standalone.mjs";
import {
  TIME_RANGES,
  TIME_RANGE_INTERVALS,
  TIME_RANGE_INTERVAL_MS,
  bucketCountForRange,
  intervalMilliseconds,
} from "./.compiled-metric-axes.mjs";

let failures = 0;
function check(name, fn) {
  try {
    fn();
    recordCase(name, true);
    console.log(`  ok: ${name}`);
  } catch (err) {
    recordCase(name, false);
    failures++;
    console.log(`  FAIL: ${name}\n    ${err.message}`);
  }
}

check("ranges include 15m and 30d and stay within the API maximum", () => {
  assert.deepEqual([...TIME_RANGES], ["15m", "1h", "6h", "24h", "7d", "30d"]);
});

check("every range stays inside the 600-bucket budget", () => {
  for (const range of TIME_RANGES) {
    const buckets = bucketCountForRange(range);
    assert.ok(buckets <= 600, `${range} needs ${buckets} buckets`);
  }
  // 30d must ask for 6h: 1h would be 720 buckets and be refused with 422.
  assert.equal(TIME_RANGE_INTERVALS["30d"], "6h");
  assert.equal(bucketCountForRange("30d"), 120);
});

check("intervals convert to milliseconds", () => {
  assert.equal(intervalMilliseconds("1m"), 60_000);
  assert.equal(intervalMilliseconds("15m"), 900_000);
  assert.equal(intervalMilliseconds("6h"), 21_600_000);
  assert.equal(intervalMilliseconds("nonsense"), TIME_RANGE_INTERVAL_MS["1h"]);
});

check("each axis mode plots its own series with the selected statistic", () => {
  assert.deepEqual(seriesForStat("percent", "avg").slice(0, 2), ["cpu_percent", "memory_percent"]);
  assert.deepEqual(seriesForStat("percent", "peak").slice(0, 2), ["cpu_peak", "memory_peak"]);
  assert.deepEqual(seriesForStat("throughput", "avg"), [
    "net_recv_rate",
    "net_sent_rate",
    "disk_read_rate",
    "disk_write_rate",
  ]);
  assert.deepEqual(seriesForStat("load", "peak"), ["load_1_peak", "load_5_peak", "load_15_peak"]);
});

check("alignment fields cover both statistics and rate coverage", () => {
  const fields = alignmentFields("throughput");
  for (const field of ["net_recv_rate", "net_recv_peak", "net_recv_coverage"]) {
    assert.ok(fields.includes(field), `missing ${field}`);
  }
  assert.equal(alignmentFields("percent").length, 8);
});

check("axis values format with the unit of their mode", () => {
  // Avoid a decimal tie: 42.55 is not exactly representable in binary floating
  // point, so assert values that round unambiguously.
  assert.equal(formatAxisValue("percent", 42.5), "42.5%");
  assert.equal(formatAxisValue("percent", 42.56), "42.6%");
  assert.equal(formatAxisValue("load", 1.234), "1.23");
  assert.equal(formatAxisValue("throughput", 2048), "2.00 KiB/s");
  assert.equal(formatAxisValue("percent", null), "—");
  assert.equal(formatAxisValue("percent", Number.NaN), "—");
  assert.equal(formatBytesPerSecond(512), "512 B");
  assert.equal(formatBytesPerSecond(1024 * 1024 * 3), "3.00 MiB");
});

check("zero is a value and null is a gap", () => {
  // The formatter must not turn a real zero into a dash, or a gap into zero.
  assert.equal(formatAxisValue("throughput", 0), "0 B/s");
  assert.equal(formatAxisValue("load", 0), "0.00");
  assert.equal(formatAxisValue("throughput", null), "—");
});

check("bucket coverage reports only finite numbers", () => {
  assert.equal(bucketCoverage({ net_recv_coverage: 60 }, "net_recv_coverage"), 60);
  assert.equal(bucketCoverage({ net_recv_coverage: null }, "net_recv_coverage"), null);
  assert.equal(bucketCoverage({}, "net_recv_coverage"), null);
  assert.equal(bucketCoverage({ net_recv_coverage: Number.POSITIVE_INFINITY }, "net_recv_coverage"), null);
});

check("a coarsened or downsampled response is never silent", () => {
  const notice = downsampleNotice({
    requestedInterval: "1h",
    actualInterval: "6h",
    coarsenReason: "bucket_budget",
    stat: "avg",
    coverage: 1,
    source: "rollup",
    downsampled: true,
  });
  assert.ok(notice, "expected a visible notice");
  assert.equal(notice.params.actual, "6h");
  assert.equal(notice.params.requested, "1h");

  // An auto query that coarsens is still reported, because the served
  // granularity differs from what the chart's bucket grid implies.
  const auto = downsampleNotice({
    requestedInterval: "auto",
    actualInterval: "6h",
    coarsenReason: "",
    stat: "avg",
    coverage: 1,
    source: "rollup",
    downsampled: true,
  });
  assert.ok(auto, "an auto query that was downsampled still needs a notice");

  const unchanged = downsampleNotice({
    requestedInterval: "6h",
    actualInterval: "6h",
    coarsenReason: "",
    stat: "avg",
    coverage: 1,
    source: "raw",
    downsampled: false,
  });
  assert.equal(unchanged, null);
  assert.equal(downsampleNotice(null), null);
});

check("coverage formats as a whole percentage", () => {
  assert.equal(formatCoverage(0.423), "42%");
  assert.equal(formatCoverage(1), "100%");
  assert.equal(formatCoverage(null), null);
});

check("series stay distinguishable without colour", () => {
  const patterns = [0, 1, 2, 3, 4].map((index) => seriesPattern(index));
  assert.deepEqual(patterns[0], { dash: "", marker: "circle" });
  assert.equal(patterns[1].dash, "6 3");
  assert.equal(patterns[2].marker, "triangle");
  assert.notEqual(patterns[3].dash, patterns[1].dash);
  // The palette wraps for longer series lists instead of returning undefined.
  assert.deepEqual(patterns[4], patterns[0]);
});

check("every axis series has a label key", () => {
  for (const [mode, series] of Object.entries(AXIS_SERIES)) {
    series.forEach((_, index) => {
      const key = seriesLabelKey(mode, index);
      assert.match(key, /^metrics\.series\./);
    });
  }
});

check("the chart description carries name, range, granularity, statistic, unit and freshness", () => {
  const built = chartDescription({
    title: "Usage (%)",
    from: "2026-01-02T00:00:00.000Z",
    to: "2026-01-02T06:00:00.000Z",
    timeZone: "UTC",
    actualInterval: "6h",
    stat: "peak",
    unit: "%",
    lastSampleAt: "2026-01-02T05:30:00.000Z",
    now: new Date("2026-01-02T06:00:00.000Z"),
  });
  assert.equal(built.key, "metrics.chart.description");
  for (const field of ["title", "from", "to", "timezone", "interval", "stat", "unit", "freshness"]) {
    assert.ok(built.params[field] !== undefined, `missing ${field}`);
  }
  assert.equal(built.params.interval, "6h");
  assert.equal(built.params.stat, "peak");
  assert.equal(built.params.unit, "%");
  assert.equal(built.params.freshness, "30");
});

check("a never-collected chart reports its freshness as unknown", () => {
  const built = chartDescription({
    title: "Load", from: "a", to: "b", timeZone: "UTC", actualInterval: "1m",
    stat: "avg", unit: "", lastSampleAt: null,
  });
  assert.equal(built.params.freshness, "never");
});

check("axis units match the axis mode", () => {
  assert.equal(axisUnit("percent"), "%");
  assert.equal(axisUnit("throughput"), "B/s");
  assert.equal(axisUnit("load"), "");
});

check("the data table caption names the range, granularity, statistic and unit", () => {
  const built = dataTableCaption({
    title: "Throughput",
    from: "2026-01-02T00:00:00.000Z",
    to: "2026-01-02T06:00:00.000Z",
    interval: "5m",
    stat: "avg",
    unit: "B/s",
  });
  assert.equal(built.key, "metrics.dataTable.caption");
  assert.equal(built.params.interval, "5m");
  assert.equal(built.params.stat, "avg");
  assert.equal(built.params.unit, "B/s");
  assert.match(built.params.from, /2026-01-02 00:00Z/);
});

check("coverage cells distinguish partial sampling from no sample", () => {
  // A rate axis with half the bucket covered.
  const half = coverageCell("throughput", [10], 30, 60_000);
  assert.equal(half.key, "metrics.dataTable.coverageFraction");
  assert.equal(half.params.percent, "50");
  // No coverage seconds at all: not sampled, never 0%.
  assert.equal(coverageCell("throughput", [10], null, 60_000).key, "metrics.dataTable.notSampled");
  assert.equal(coverageCell("throughput", [10], 0, 60_000).key, "metrics.dataTable.notSampled");
  // Gauge axes report sample presence instead.
  assert.equal(coverageCell("percent", [1, null], null, 60_000).key, "metrics.dataTable.sampled");
  assert.equal(coverageCell("percent", [null, null], null, 60_000).key, "metrics.dataTable.notSampled");
});

check("the data view pages at 50 rows and clamps out-of-range pages", () => {
  assert.equal(DATA_TABLE_PAGE_SIZE, 50);
  const first = pageSlice(120, 1);
  assert.deepEqual([first.start, first.end, first.pages, first.page], [0, 50, 3, 1]);
  const last = pageSlice(120, 3);
  assert.deepEqual([last.start, last.end], [100, 120]);
  const clamped = pageSlice(120, 99);
  assert.deepEqual([clamped.page, clamped.start, clamped.end], [3, 100, 120]);
  assert.deepEqual([pageSlice(120, 0).page, pageSlice(120, -5).page], [1, 1]);
  assert.deepEqual([pageSlice(0, 1).pages, pageSlice(0, 1).end], [1, 0]);
});

check("window averages are sample-weighted, not the mean of bucket means", () => {
  // One bucket with a single 0% sample and one with 360 samples at 100%: the
  // sample-weighted average is 99.7%, while averaging the bucket means gives 50%.
  const points = [
    { bucket: "a", cpu_sum: 0, cpu_count: 1 },
    { bucket: "b", cpu_sum: 36000, cpu_count: 360 },
  ];
  const weighted = weightedMean(points, "cpu_sum", "cpu_count");
  assert.ok(Math.abs(weighted - 99.723) < 0.01, `weighted = ${weighted}`);
  const meanOfMeans = (0 / 1 + 36000 / 360) / 2;
  assert.equal(meanOfMeans, 50);
  assert.notEqual(Math.round(weighted), Math.round(meanOfMeans));
});

check("payloads without statistics fall back to the flat mean", () => {
  // A rollup written before the sums existed has values but no sum/count: the old
  // behaviour (mean of the collected values) is kept instead of a missing average.
  const legacy = [{ cpu_percent: 10 }, { cpu_percent: null }, { cpu_percent: 40 }];
  assert.equal(weightedMean(legacy, "cpu_sum", "cpu_count", "cpu_percent"), 25);
  // Statistics win whenever they are present, even mixed with legacy points.
  const mixed = [{ cpu_percent: 10 }, { cpu_percent: 90, cpu_sum: 900, cpu_count: 10 }];
  assert.equal(weightedMean(mixed, "cpu_sum", "cpu_count", "cpu_percent"), 90);
  assert.equal(weightedMean([], "cpu_sum", "cpu_count", "cpu_percent"), null);
});

check("a window with no usable sum/count reports no average", () => {
  assert.equal(weightedMean([], "cpu_sum", "cpu_count"), null);
  assert.equal(weightedMean([{ cpu_sum: null, cpu_count: null }], "cpu_sum", "cpu_count"), null);
  assert.equal(weightedMean([{ cpu_sum: 5, cpu_count: 0 }], "cpu_sum", "cpu_count"), null);
  assert.equal(weightedMean([{ cpu_sum: Number.NaN, cpu_count: 2 }], "cpu_sum", "cpu_count"), null);
});

console.log(failures === 0 ? "\nmonitoring-chart-config: ALL PASS" : `\nmonitoring-chart-config: ${failures} FAILURES`);
nameSuite("monitoring-chart-config", import.meta.url);
writeReport();

process.exit(failures === 0 ? 0 : 1);

