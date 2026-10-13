/**
 * Axis, statistic and formatting rules for the monitoring charts.
 *
 * Pure and dependency-free so `tests/run.sh` can compile and unit-test it.
 */

import type { AxisMode, MetricStat } from "../../../types/metric-axes";

/** Minimal shapes the helpers need, so this module stays import-light. */
interface PointLike {
	[field: string]: unknown;
}
interface MetaLike {
	requestedInterval: string;
	actualInterval: string;
	coarsenReason: string;
	stat: MetricStat;
	coverage: number;
	source: "raw" | "rollup";
	downsampled: boolean;
}

/** One plotted series: the average column, its peak column and the unit. */
export interface AxisSeries {
	/** Field names in the aligned series (avg and peak variants). */
	avgField: string;
	peakField: string;
	/** Coverage column, when the series is a rate. */
	coverageField?: string;
	/** i18n label key suffix under `metrics.series.*`. */
	label: string;
	unit: string;
}

/** Percentage axes: CPU, memory, disk and swap usage. */
const PERCENT_SERIES: AxisSeries[] = [
	{ avgField: "cpu_percent", peakField: "cpu_peak", label: "cpu", unit: "%" },
	{
		avgField: "memory_percent",
		peakField: "memory_peak",
		label: "memory",
		unit: "%",
	},
	{
		avgField: "disk_percent",
		peakField: "disk_peak",
		label: "disk",
		unit: "%",
	},
	{
		avgField: "swap_percent",
		peakField: "swap_peak",
		label: "swap",
		unit: "%",
	},
];

/** Throughput axes: network and disk rates with their coverage. */
const THROUGHPUT_SERIES: AxisSeries[] = [
	{
		avgField: "net_recv_rate",
		peakField: "net_recv_peak",
		coverageField: "net_recv_coverage",
		label: "net_recv",
		unit: "B/s",
	},
	{
		avgField: "net_sent_rate",
		peakField: "net_sent_peak",
		coverageField: "net_sent_coverage",
		label: "net_sent",
		unit: "B/s",
	},
	{
		avgField: "disk_read_rate",
		peakField: "disk_read_peak",
		coverageField: "disk_read_coverage",
		label: "disk_read",
		unit: "B/s",
	},
	{
		avgField: "disk_write_rate",
		peakField: "disk_write_peak",
		coverageField: "disk_write_coverage",
		label: "disk_write",
		unit: "B/s",
	},
];

/** Load axes: the three load averages. */
const LOAD_SERIES: AxisSeries[] = [
	{ avgField: "load_1", peakField: "load_1_peak", label: "load_1", unit: "" },
	{ avgField: "load_5", peakField: "load_5_peak", label: "load_5", unit: "" },
	{
		avgField: "load_15",
		peakField: "load_15_peak",
		label: "load_15",
		unit: "",
	},
];

export const AXIS_SERIES: Record<AxisMode, AxisSeries[]> = {
	percent: PERCENT_SERIES,
	throughput: THROUGHPUT_SERIES,
	load: LOAD_SERIES,
};

/**
 * Series selected for a mode, with the field each one plots for the requested
 * statistic. Peak falls back to the average field for points that predate the
 * peak columns, so an older cached payload still renders.
 */
export function seriesForStat(mode: AxisMode, stat: MetricStat): string[] {
	return AXIS_SERIES[mode].map((series) =>
		stat === "peak" ? series.peakField : series.avgField,
	);
}

/** All fields that must be aligned for a mode (both statistics + coverage). */
export function alignmentFields(mode: AxisMode): string[] {
	const fields: string[] = [];
	for (const series of AXIS_SERIES[mode]) {
		fields.push(series.avgField, series.peakField);
		if (series.coverageField) fields.push(series.coverageField);
	}
	return fields;
}

/** Formats one value for the axis and the tooltip. */
export function formatAxisValue(
	mode: AxisMode,
	value: number | null | undefined,
): string {
	if (value === null || value === undefined || !Number.isFinite(value))
		return "—";
	switch (mode) {
		case "percent":
			return `${value.toFixed(1)}%`;
		case "throughput":
			return `${formatBytesPerSecond(value)}/s`;
		case "load":
			return value.toFixed(2);
	}
}

/** SI-scaled byte formatting shared by the throughput axis and tooltips. */
export function formatBytesPerSecond(bytesPerSecond: number): string {
	const units = ["B", "KiB", "MiB", "GiB", "TiB"];
	let value = Math.abs(bytesPerSecond);
	let unit = 0;
	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024;
		unit++;
	}
	const sign = bytesPerSecond < 0 ? "-" : "";
	const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
	return `${sign}${value.toFixed(digits)} ${units[unit]}`;
}

/** i18n key suffix describing a series, for legends and the data table. */
export function seriesLabelKey(mode: AxisMode, index: number): string {
	const series = AXIS_SERIES[mode][index];
	return series ? `metrics.series.${series.label}` : "metrics.series.unknown";
}

/** Coverage fraction of a rate bucket, or null when the bucket has no samples. */
export function bucketCoverage(point: PointLike, field: string): number | null {
	const value = (point as unknown as Record<string, unknown>)[field];
	return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/**
 * Visible notice when the backend served a different granularity than asked, or
 * downsampled the series. Returns an i18n key plus its parameters, never a
 * silent change (REQUIREMENTS §4.2.1).
 */
export function downsampleNotice(meta: MetaLike | null | undefined): {
	key: string;
	params: Record<string, string>;
} | null {
	if (!meta) return null;
	const changed =
		meta.actualInterval !== meta.requestedInterval &&
		meta.requestedInterval !== "auto";
	if (!changed && !meta.downsampled) return null;
	return {
		key:
			meta.source === "rollup"
				? "metrics.notice.rollupSource"
				: "metrics.notice.downsampled",
		params: {
			requested: meta.requestedInterval,
			actual: meta.actualInterval,
			reason: meta.coarsenReason || "budget",
		},
	};
}

/** Coverage as a whole-percent string, or null when unknown. */
export function formatCoverage(
	coverage: number | null | undefined,
): string | null {
	if (coverage === null || coverage === undefined || !Number.isFinite(coverage))
		return null;
	return `${Math.round(coverage * 100)}%`;
}

/**
 * Non-colour series distinction (REQUIREMENTS §4.5): every series gets its own
 * dash pattern and marker, so the chart is readable without relying on hue and
 * remains distinguishable at low contrast or in greyscale.
 */
export const SERIES_PATTERNS: readonly {
	dash: string;
	marker: "circle" | "square" | "triangle" | "diamond";
}[] = [
	{ dash: "", marker: "circle" },
	{ dash: "6 3", marker: "square" },
	{ dash: "2 3", marker: "triangle" },
	{ dash: "10 3 2 3", marker: "diamond" },
];

export function seriesPattern(index: number): { dash: string; marker: string } {
	const pattern =
		SERIES_PATTERNS[
			((index % SERIES_PATTERNS.length) + SERIES_PATTERNS.length) %
				SERIES_PATTERNS.length
		];
	return { dash: pattern.dash, marker: pattern.marker };
}

/** Rows per page of the accessible data view (REQUIREMENTS §4.5). */
export const DATA_TABLE_PAGE_SIZE = 50;

/** Page slice of a row count, clamped to the available pages. */
export function pageSlice(total: number, page: number, size: number = DATA_TABLE_PAGE_SIZE): {
  start: number;
  end: number;
  pages: number;
  page: number;
} {
  const pages = Math.max(1, Math.ceil(total / size));
  const current = Math.min(Math.max(1, Math.trunc(page) || 1), pages);
  const start = (current - 1) * size;
  return { start, end: Math.min(start + size, total), pages, page: current };
}

/**
 * Screen-reader description of one chart (REQUIREMENTS §4.5): name, range with
 * its time zone, the granularity the backend actually served, the statistic, the
 * unit and how fresh the data is. Returns an i18n key plus its parameters so the
 * wording stays translatable.
 */
export function chartDescription(input: {
  title: string;
  from: string;
  to: string;
  timeZone: string;
  actualInterval: string;
  stat: MetricStat;
  unit: string;
  lastSampleAt: string | null;
  now?: Date;
}): { key: string; params: Record<string, string> } {
  const locale = "en-GB";
  const format = (iso: string) => {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return iso;
    return date.toLocaleString(locale, { hour12: false });
  };
  let freshness = "never";
  if (input.lastSampleAt) {
    const age = (input.now ?? new Date()).getTime() - Date.parse(input.lastSampleAt);
    freshness = Number.isFinite(age) ? `${Math.max(0, Math.round(age / 60000))}` : "unknown";
  }
  return {
    key: "metrics.chart.description",
    params: {
      title: input.title,
      from: format(input.from),
      to: format(input.to),
      timezone: input.timeZone,
      interval: input.actualInterval,
      stat: input.stat,
      unit: input.unit,
      freshness,
    },
  };
}

/** Unit label for an axis mode, used by the description and the data table. */
export function axisUnit(mode: AxisMode): string {
  switch (mode) {
    case "percent":
      return "%";
    case "throughput":
      return "B/s";
    default:
      return "";
  }
}

/**
 * Caption of the accessible data view: range, granularity, statistic and unit
 * (REQUIREMENTS §4.5), as an i18n key with parameters.
 */
export function dataTableCaption(input: {
  title: string;
  from: string;
  to: string;
  interval: string;
  stat: MetricStat;
  unit: string;
}): { key: string; params: Record<string, string> } {
  const short = (iso: string) => {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return iso;
    return date.toISOString().replace("T", " ").slice(0, 16) + "Z";
  };
  return {
    key: "metrics.dataTable.caption",
    params: {
      title: input.title,
      from: short(input.from),
      to: short(input.to),
      interval: input.interval,
      stat: input.stat,
      unit: input.unit || "—",
    },
  };
}

/**
 * One row's coverage cell: the valid sampled fraction of the bucket for rate
 * axes, or a sampled/not-sampled marker for the others, so the table never
 * implies data that was not collected.
 */
export function coverageCell(
  mode: AxisMode,
  values: readonly (number | null)[],
  coverageSeconds: number | null,
  intervalMs: number,
): { key: string; params?: Record<string, string> } {
  if (mode === "throughput") {
    if (coverageSeconds === null || !Number.isFinite(coverageSeconds) || intervalMs <= 0) {
      return { key: "metrics.dataTable.notSampled" };
    }
    const fraction = Math.max(0, Math.min(1, coverageSeconds / (intervalMs / 1000)));
    const base = { key: "metrics.dataTable.coverageFraction", params: { percent: `${Math.round(fraction * 100)}` } };
    return fraction === 0 ? { key: "metrics.dataTable.notSampled" } : base;
  }
  const sampled = values.some((value) => value !== null && Number.isFinite(value));
  return { key: sampled ? "metrics.dataTable.sampled" : "metrics.dataTable.notSampled" };
}

/**
 * Sample-weighted average over a window: Σsum/Σcount from the per-bucket
 * statistics. Averaging bucket means would let a bucket with a single sample
 * outweigh one with hundreds (F06), which is exactly the "平均桶均值" bug.
 *
 * Returns null when no bucket contributed a usable sum/count pair.
 */
export function weightedMean<T extends object>(
  points: readonly T[],
  sumField: string,
  countField: string,
): number | null {
  let sum = 0;
  let count = 0;
  for (const point of points) {
    const fields = point as Record<string, unknown>;
    const bucketSum = fields[sumField];
    const bucketCount = fields[countField];
    if (
      typeof bucketSum === "number" && Number.isFinite(bucketSum) &&
      typeof bucketCount === "number" && Number.isFinite(bucketCount) && bucketCount > 0
    ) {
      sum += bucketSum;
      count += bucketCount;
    }
  }
  return count === 0 ? null : sum / count;
}
