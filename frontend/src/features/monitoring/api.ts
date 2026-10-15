import { z } from "zod";

import { apiClient } from "../../lib/api-client";
import {
  TIME_RANGE_INTERVAL_MS,
  TIME_RANGE_INTERVALS,
  intervalMilliseconds,
  type MetricStat,
  type TimeRange,
} from "../../types/metric-axes";
import { type MetricPoint, type MetricQueryMeta, TIME_RANGE_MAP } from "../../types/metrics";

const MetricPointSchema = z.object({
  time: z.string(),
  sampled_at: z.string().nullable().optional(),
  cpu_percent: z.number().nullable(),
  memory_percent: z.number().nullable(),
  disk_percent: z.number().nullable(),
  load_1: z.number().nullable(),
  load_5: z.number().nullable(),
  load_15: z.number().nullable(),
  swap_percent: z.number().nullable(),
  net_recv_rate: z.number().nullable(),
  net_sent_rate: z.number().nullable(),
  disk_read_rate: z.number().nullable(),
  disk_write_rate: z.number().nullable(),

  cpu_peak: z.number().nullable().optional(),
  memory_peak: z.number().nullable().optional(),
  disk_peak: z.number().nullable().optional(),
  swap_peak: z.number().nullable().optional(),
  load_1_peak: z.number().nullable().optional(),
  load_5_peak: z.number().nullable().optional(),
  load_15_peak: z.number().nullable().optional(),
  net_recv_peak: z.number().nullable().optional(),
  net_sent_peak: z.number().nullable().optional(),
  disk_read_peak: z.number().nullable().optional(),
  disk_write_peak: z.number().nullable().optional(),

  // Per-bucket statistics (F06): the window average is Σsum/Σcount, so the
  // client must keep them instead of letting the schema strip them.
  cpu_sum: z.number().nullable().optional(),
  cpu_count: z.number().nullable().optional(),
  memory_percent_sum: z.number().nullable().optional(),
  memory_percent_count: z.number().nullable().optional(),
  disk_percent_sum: z.number().nullable().optional(),
  disk_percent_count: z.number().nullable().optional(),
  swap_percent_sum: z.number().nullable().optional(),
  swap_percent_count: z.number().nullable().optional(),
  load1_sum: z.number().nullable().optional(),
  load1_count: z.number().nullable().optional(),
  load5_sum: z.number().nullable().optional(),
  load5_count: z.number().nullable().optional(),
  load15_sum: z.number().nullable().optional(),
  load15_count: z.number().nullable().optional(),

  net_recv_coverage: z.number().nullable().optional(),
  net_sent_coverage: z.number().nullable().optional(),
  disk_read_coverage: z.number().nullable().optional(),
  disk_write_coverage: z.number().nullable().optional(),
});

/** The response envelope: points plus what the backend actually served. */
const MetricQueryResponseSchema = z.object({
  server_id: z.number(),
  from: z.string(),
  to: z.string(),
  requested_interval: z.string(),
  actual_interval: z.string(),
  coarsen_reason: z.string(),
  stat: z.enum(["avg", "peak"]),
  coverage: z.number(),
  source: z.enum(["raw", "rollup"]),
  downsampled: z.boolean(),
  points: z.array(MetricPointSchema),
});

/** The exact window a metrics response was fetched for. */
export interface MetricWindow {
  from: string;
  to: string;
  intervalMs: number;
}

export interface MetricsResult {
  window: MetricWindow;
  points: MetricPoint[];
  meta: MetricQueryMeta;
}

export interface GetMetricsOptions {
  stat?: MetricStat;
  /** Explicit granularity (the user picked one); defaults to the range's. */
  interval?: string;
  /** Required when an explicit granularity is sent: the UI accepts coarsening. */
  allowCoarsen?: boolean;
}

/**
 * Fetches aggregated metrics and returns the window, the points and the query
 * metadata.
 *
 * The window travels with the data so the chart aligns its bucket grid to the
 * same instants the backend queried — recomputing `now` at render time would
 * shift the two apart. The metadata is kept so the UI can show which granularity
 * the backend really used instead of implying a finer sampling than exists.
 */
export async function getMetrics(
  serverId: number,
  timeRange: TimeRange,
  options: GetMetricsOptions = {},
): Promise<MetricsResult> {
  const now = new Date();
  const from = new Date(now.getTime() - TIME_RANGE_MAP[timeRange].hours * 3600 * 1000);
  const fromIso = from.toISOString();
  const toIso = now.toISOString();

  // The default request is `auto`: the backend picks the finest granularity the
  // bucket budget allows, so a denser collection configuration (for example
  // MONITOR_INTERVAL=30) can still be served from the 6h rollup instead of being
  // refused with 422 (R14). A granularity the user picked explicitly is sent as
  // such and the UI accepts coarsening, showing what was really used.
  const interval = options.interval ?? "auto";
  const params = new URLSearchParams({ from: fromIso, to: toIso, interval, stat: options.stat ?? "avg" });
  if (options.interval) {
    params.set("allow_coarsen", options.allowCoarsen === false ? "false" : "true");
  }

  const res = await apiClient.get<unknown>(
    `/api/v1/servers/${serverId}/metrics?${params.toString()}`,
  );
  const parsed = MetricQueryResponseSchema.parse(res);

  const intervalMs = intervalMilliseconds(parsed.actual_interval);

  return {
    window: { from: parsed.from, to: parsed.to, intervalMs },
    points: parsed.points,
    meta: {
      requestedInterval: parsed.requested_interval,
      actualInterval: parsed.actual_interval,
      coarsenReason: parsed.coarsen_reason,
      stat: parsed.stat,
      coverage: parsed.coverage,
      source: parsed.source,
      downsampled: parsed.downsampled,
    },
  };
}

export { TIME_RANGE_INTERVAL_MS, TIME_RANGE_INTERVALS };
