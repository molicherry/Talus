import { z } from "zod";

import { apiClient } from "../../lib/api-client";
import {
  type MetricPoint,
  TIME_RANGE_INTERVAL_MS,
  TIME_RANGE_INTERVALS,
  TIME_RANGE_MAP,
  type TimeRange,
} from "../../types/metrics";

const MetricPointSchema = z.object({
  time: z.string(),
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
}

/**
 * Fetches aggregated metrics and returns the window alongside the points.
 *
 * The window travels with the data so the chart aligns its bucket grid to the
 * same instants the backend queried — recomputing `now` at render time would
 * shift the two apart.
 */
export async function getMetrics(serverId: number, timeRange: TimeRange): Promise<MetricsResult> {
  const now = new Date();
  const from = new Date(now.getTime() - TIME_RANGE_MAP[timeRange].hours * 3600 * 1000);
  const fromIso = from.toISOString();
  const toIso = now.toISOString();

  const params = new URLSearchParams({
    from: fromIso,
    to: toIso,
    interval: TIME_RANGE_INTERVALS[timeRange],
  });

  const res = await apiClient.get<MetricPoint[]>(
    `/api/v1/servers/${serverId}/metrics?${params.toString()}`,
  );

  return {
    window: { from: fromIso, to: toIso, intervalMs: TIME_RANGE_INTERVAL_MS[timeRange] },
    points: z.array(MetricPointSchema).parse(res),
  };
}
