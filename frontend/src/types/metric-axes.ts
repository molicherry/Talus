/**
 * Dependency-free metric axis and granularity vocabulary.
 *
 * Kept free of imports so `tests/run.sh` can compile this module (and the pure
 * helpers that use it) in isolation, following the same pattern as
 * `features/monitoring/lib/series.ts`.
 */

/** The statistic the chart renders; both are always fetched. */
export type MetricStat = "avg" | "peak";

/** Axis groupings offered by the chart. */
export type AxisMode = "percent" | "throughput" | "load";

/** Selectable time ranges, shortest first. 30d is the API's maximum. */
export type TimeRange = "15m" | "1h" | "6h" | "24h" | "7d" | "30d";

export const TIME_RANGES: readonly TimeRange[] = ["15m", "1h", "6h", "24h", "7d", "30d"];

export const TIME_RANGE_HOURS: Record<TimeRange, number> = {
  "15m": 0.25,
  "1h": 1,
  "6h": 6,
  "24h": 24,
  "7d": 168,
  "30d": 720,
};

/**
 * Granularity requested per range. 30d asks for 6h because the API budget is
 * 600 buckets: 30d at 1h would be 720 buckets and would be refused with 422
 * `metrics_query_budget_exceeded`.
 */
export const TIME_RANGE_INTERVALS: Record<TimeRange, string> = {
  "15m": "1m",
  "1h": "1m",
  "6h": "5m",
  "24h": "15m",
  "7d": "1h",
  "30d": "6h",
};

/** The same granularities in milliseconds, for epoch-aligned bucket grids. */
export const TIME_RANGE_INTERVAL_MS: Record<TimeRange, number> = {
  "15m": 60_000,
  "1h": 60_000,
  "6h": 300_000,
  "24h": 900_000,
  "7d": 3_600_000,
  "30d": 21_600_000,
};

/** Milliseconds of a backend granularity such as "1m", "15m", "1h", "6h". */
export function intervalMilliseconds(interval: string): number {
  const match = /^(\d+)([mh])$/.exec(interval);
  if (!match) return TIME_RANGE_INTERVAL_MS["1h"];
  const value = Number(match[1]);
  return match[2] === "h" ? value * 3_600_000 : value * 60_000;
}

/** Buckets a range occupies at its default granularity (for the budget check). */
export function bucketCountForRange(range: TimeRange): number {
  const spanMs = TIME_RANGE_HOURS[range] * 3_600_000;
  return Math.ceil(spanMs / TIME_RANGE_INTERVAL_MS[range]);
}
