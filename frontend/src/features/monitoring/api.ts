import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import {
  type MetricPoint,
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

export async function getMetrics(serverId: number, timeRange: TimeRange): Promise<MetricPoint[]> {
  const now = new Date();
  const from = new Date(now.getTime() - TIME_RANGE_MAP[timeRange].hours * 3600 * 1000);

  const params = new URLSearchParams({
    from: from.toISOString(),
    to: now.toISOString(),
    interval: TIME_RANGE_INTERVALS[timeRange],
  });

  const res = await apiClient.get<MetricPoint[]>(
    `/api/v1/servers/${serverId}/metrics?${params.toString()}`,
  );
  return z.array(MetricPointSchema).parse(res);
}
