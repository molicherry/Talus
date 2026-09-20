/**
 * Time-series alignment for the monitoring charts.
 *
 * The metrics endpoint aggregates with `time_bucket(...) GROUP BY bucket`, so
 * it only returns buckets that actually contain samples. Plotting those
 * directly compresses gaps (and a gap can even be drawn as a straight line).
 * This module rebuilds the full bucket grid for the requested window and
 * leaves missing buckets as `null`, which the chart renders as a break.
 *
 * Deliberately dependency-free: `tests/run.sh` compiles it as a single file.
 */

export interface SeriesWindow {
  from: string;
  to: string;
  intervalMs: number;
}

export interface AlignedSeries {
  /** ISO start of every bucket across the window, oldest first. */
  times: string[];
  /** Field name -> values aligned 1:1 with `times`; `null` = no sample. */
  series: Record<string, Array<number | null>>;
  /**
   * Latest *raw* sample time in the window (never a filled bucket). Used for
   * "last collected", which must not drift as empty buckets are appended.
   */
  lastSampleTime: string | null;
}

function finite(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/**
 * Align `points` onto a dense bucket grid built from `window`.
 *
 * Buckets are floored to the Unix epoch, which is exactly what PostgreSQL's
 * `time_bucket` does; for the app's 1m/5m/15m/1h intervals that is the same as
 * aligning to UTC clock boundaries. Real zeroes are preserved — only absent
 * data becomes `null`.
 */
export function buildAlignedSeries<T extends { time: string }>(
  points: readonly T[],
  window: SeriesWindow,
  fields: readonly string[],
): AlignedSeries {
  const interval = window.intervalMs;
  const fromMs = Date.parse(window.from);
  const toMs = Date.parse(window.to);

  const series: Record<string, Array<number | null>> = {};
  for (const field of fields) series[field] = [];

  // Without a usable window or interval there is nothing to align against.
  if (!Number.isFinite(interval) || interval <= 0 || !Number.isFinite(fromMs) || !Number.isFinite(toMs) || toMs < fromMs) {
    return { times: [], series, lastSampleTime: null };
  }

  const start = Math.floor(fromMs / interval) * interval;
  const end = Math.floor(toMs / interval) * interval;
  const count = Math.floor((end - start) / interval) + 1;

  const times: string[] = new Array(count);
  for (let i = 0; i < count; i++) {
    times[i] = new Date(start + i * interval).toISOString();
    for (const field of fields) series[field][i] = null;
  }

  let lastSampleMs: number | null = null;
  for (const point of points) {
    const t = Date.parse(point.time);
    if (!Number.isFinite(t)) continue;

    if (lastSampleMs === null || t > lastSampleMs) lastSampleMs = t;

    const bucket = Math.floor(t / interval) * interval;
    if (bucket < start || bucket > end) continue;
    const index = Math.floor((bucket - start) / interval);

    const source = point as unknown as Record<string, unknown>;
    for (const field of fields) {
      // A later sample wins for a duplicated bucket; an explicit null in the
      // point overwrites with null (still "no sample").
      series[field][index] = finite(source[field]);
    }
  }

  return {
    times,
    series,
    lastSampleTime: lastSampleMs === null ? null : new Date(lastSampleMs).toISOString(),
  };
}
