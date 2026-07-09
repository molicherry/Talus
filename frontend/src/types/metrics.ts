import i18n from "../i18n";

export interface MetricPoint {
  time: string;
  cpu_percent: number | null;
  memory_percent: number | null;
  disk_percent: number | null;
  load_1: number | null;
  load_5: number | null;
  load_15: number | null;
  swap_percent: number | null;
  net_recv_rate: number | null;
  net_sent_rate: number | null;
  disk_read_rate: number | null;
  disk_write_rate: number | null;
}

export type TimeRange = "1h" | "6h" | "24h" | "7d";

export const TIME_RANGE_MAP: Record<TimeRange, { label: string; hours: number }> = {
  "1h": { label: i18n.t("timeRange.1h"), hours: 1 },
  "6h": { label: i18n.t("timeRange.6h"), hours: 6 },
  "24h": { label: i18n.t("timeRange.24h"), hours: 24 },
  "7d": { label: i18n.t("timeRange.7d"), hours: 168 },
};

export const TIME_RANGE_INTERVALS: Record<TimeRange, string> = {
  "1h": "1m",
  "6h": "5m",
  "24h": "15m",
  "7d": "1h",
};
