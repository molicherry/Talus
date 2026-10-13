import i18n from "../i18n";
import type { MetricStat, TimeRange } from "./metric-axes";

export interface MetricPoint {
	time: string;
	/**
	 * Latest real sample inside the bucket. Bucket starts are aggregation
	 * boundaries, so freshness and "last collected" must use this when present.
	 */
	sampled_at?: string | null;
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

	// Observed maxima of the sampled interval, retained separately from the
	// average so long-range charts can still show peaks (REQ §8.3).
	cpu_peak?: number | null;
	memory_peak?: number | null;
	disk_peak?: number | null;
	swap_peak?: number | null;
	load_1_peak?: number | null;
	load_5_peak?: number | null;
	load_15_peak?: number | null;
	net_recv_peak?: number | null;
	net_sent_peak?: number | null;
	disk_read_peak?: number | null;
	disk_write_peak?: number | null;

	// Valid coverage seconds behind the rate columns.
	net_recv_coverage?: number | null;
	net_sent_coverage?: number | null;
	disk_read_coverage?: number | null;
	disk_write_coverage?: number | null;
}

export {
	TIME_RANGE_HOURS,
	TIME_RANGE_INTERVAL_MS,
	TIME_RANGE_INTERVALS,
	TIME_RANGES,
	intervalMilliseconds,
	bucketCountForRange,
} from "./metric-axes";
export type { AxisMode, MetricStat, TimeRange } from "./metric-axes";

/** Display labels for the ranges (i18n-backed). */
export const TIME_RANGE_MAP: Record<
	TimeRange,
	{ label: string; hours: number }
> = {
	"15m": { label: i18n.t("timeRange.15m"), hours: 0.25 },
	"1h": { label: i18n.t("timeRange.1h"), hours: 1 },
	"6h": { label: i18n.t("timeRange.6h"), hours: 6 },
	"24h": { label: i18n.t("timeRange.24h"), hours: 24 },
	"7d": { label: i18n.t("timeRange.7d"), hours: 168 },
	"30d": { label: i18n.t("timeRange.30d"), hours: 720 },
};

/**
 * Query metadata returned alongside the points (REQUIREMENTS §4.2.1). It tells
 * the UI what granularity the backend actually used, whether it had to coarsen,
 * which statistic was requested, how much of the window has data, whether the
 * rollup served it and whether the series is downsampled — so nothing about the
 * response is implicit.
 */
export interface MetricQueryMeta {
	requestedInterval: string;
	actualInterval: string;
	coarsenReason: string;
	stat: MetricStat;
	coverage: number;
	source: "raw" | "rollup";
	downsampled: boolean;
}
