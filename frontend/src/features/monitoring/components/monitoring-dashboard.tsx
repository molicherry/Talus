import { useMemo, useState, useEffect, type ReactNode } from "react";

import { translateApiError } from "../../../lib/api-error";
import { Card } from "../../../components/ui/card";
import { useTranslation } from "../../../i18n";
import {
  formatPercent,
  METRIC_MISSING_LABEL,
  metricLevelFill,
  metricLevelText,
  percentLevel,
} from "../../../lib/metric-level";
import { weightedMean } from "../lib/chart-config";
import { cn } from "../../../lib/utils";
import type { TimeRange } from "../../../types/metrics";
import { useMetrics } from "../hooks/use-metrics";
import { buildAlignedSeries } from "../lib/series";
import {
  AXIS_SERIES,
  alignmentFields,
  downsampleNotice,
  formatBytesPerSecond as formatBytesPerSecondSI,
  formatCoverage,
  seriesForStat,
  seriesLabelKey,
  seriesPattern,
  axisUnit,
  chartDescription,
  coverageCell,
  dataTableCaption,
  pageSlice,
} from "../lib/chart-config";
import type { AxisMode, MetricStat } from "../../../types/metric-axes";
import { EmptyState } from "./empty-state";
import { ErrorState } from "./error-state";
import { LoadingSkeleton } from "./loading-skeleton";
import { TimeRangeSelector } from "./time-range-selector";
import { TrendChart } from "./trend-chart";

interface MonitoringDashboardProps {
  os?: string | null;
  serverId: number;
}

/**
 * A metric series keeps `null` for buckets without a sample instead of
 * coercing them to 0. Treating "not collected" as 0 previously rendered empty
 * buckets as "almost idle" and dragged averages down.
 */
type Nullable = number | null;

// Chart series use the dedicated chart palette: those tokens are verified to
// reach 3:1 against the card background in both themes (R19), while the UI accent
// blue does not on light backgrounds.
const SERIES_COLOR = {
  cpu: "var(--color-chart-1)",
  memory: "var(--color-chart-2)",
  disk: "var(--color-chart-3)",
  swap: "var(--color-chart-4)",
  rx: "var(--color-chart-1)",
  tx: "var(--color-chart-2)",
  read: "var(--color-chart-1)",
  write: "var(--color-chart-2)",
} as const;

/** Chart colour per axis series label (colour is secondary to the pattern). */
const AXIS_COLOR: Record<string, string> = {
  cpu: SERIES_COLOR.cpu,
  memory: SERIES_COLOR.memory,
  disk: SERIES_COLOR.disk,
  swap: SERIES_COLOR.swap,
  net_recv: SERIES_COLOR.rx,
  net_sent: SERIES_COLOR.tx,
  disk_read: SERIES_COLOR.read,
  disk_write: SERIES_COLOR.write,
  load_1: "var(--color-chart-1)",
  load_5: "var(--color-chart-2)",
  load_15: "var(--color-chart-3)",
};

// Fields aligned onto the bucket grid for the cards and charts below.
const SERIES_FIELDS = [
  "cpu_percent",
  "memory_percent",
  "disk_percent",
  "swap_percent",
  "net_recv_rate",
  "net_sent_rate",
  "disk_read_rate",
  "disk_write_rate",
  "load_1",
  "load_5",
  "load_15",
] as const;

/** Most recent collected sample (skips trailing buckets with no data). */
function latestOf(values: Nullable[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const value = values[i];
    if (value !== null) return value;
  }
  return null;
}

function maxOf(values: Nullable[]): number | null {
  const present = values.filter((v): v is number => v !== null);
  if (present.length === 0) return null;
  return Math.max(...present);
}

function formatBytesPerSec(bytesPerSec: number): string {
  if (bytesPerSec >= 1_000_000) return `${(bytesPerSec / 1_000_000).toFixed(1)} MB/s`;
  if (bytesPerSec >= 1_000) return `${(bytesPerSec / 1_000).toFixed(1)} KB/s`;
  return `${bytesPerSec.toFixed(1)} B/s`;
}

function formatBytesAxis(bytesPerSec: number): string {
  if (bytesPerSec >= 1_000_000) return `${(bytesPerSec / 1_000_000).toFixed(bytesPerSec >= 10_000_000 ? 0 : 1)}M`;
  if (bytesPerSec >= 1_000) return `${(bytesPerSec / 1_000).toFixed(bytesPerSec >= 10_000 ? 0 : 1)}K`;
  return `${bytesPerSec.toFixed(0)}`;
}

function makeTimeFormatter(range: TimeRange): (iso: string) => string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return (iso: string) => {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return iso;
    if (range === "7d" || range === "30d") return `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
  };
}

interface MetricStatProps {
  label: string;
  value: Nullable;
  avg: Nullable;
  max: Nullable;
  children?: ReactNode;
}

function MetricStatCard({ label, value, avg, max, children }: MetricStatProps) {
  const { t } = useTranslation();
  const level = percentLevel(value);
  return (
    <Card className="min-w-0 p-4">
      <div className="flex items-center justify-between">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </h3>
        <span
          className={cn("h-2 w-2 rounded-full", metricLevelFill[level])}
          aria-hidden="true"
        />
      </div>
      <p className={cn("mt-3 text-2xl font-semibold tabular-nums", metricLevelText[level])}>
        {formatPercent(value, 1)}
      </p>
      <div className="mt-2 flex items-center gap-3 text-xs text-muted-foreground">
        <span>
          {t("monitoring.avg")}{" "}
          <span className="font-mono tabular-nums text-foreground">{formatPercent(avg, 1)}</span>
        </span>
        <span>
          {t("monitoring.max")}{" "}
          <span className="font-mono tabular-nums text-foreground">{formatPercent(max, 1)}</span>
        </span>
      </div>
      {children}
    </Card>
  );
}

interface ThroughputCardProps {
  title: string;
  times: string[];
  primaryLabel: string;
  primaryValue: Nullable;
  primaryValues: Nullable[];
  secondaryLabel: string;
  secondaryValue: Nullable;
  secondaryValues: Nullable[];
  formatTime: (iso: string) => string;
}

function ThroughputCard({
  title,
  times,
  primaryLabel,
  primaryValue,
  primaryValues,
  secondaryLabel,
  secondaryValue,
  secondaryValues,
  formatTime,
}: ThroughputCardProps) {
  return (
    <Card className="p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-foreground">{title}</h3>
        <div className="flex items-center gap-4 text-xs">
          <span className="inline-flex items-center gap-1.5 text-muted-foreground">
            <span className="h-2 w-2 rounded-full bg-primary" aria-hidden="true" />
            {primaryLabel}
            <span className="font-mono tabular-nums text-foreground">
              {primaryValue === null ? METRIC_MISSING_LABEL : formatBytesPerSec(primaryValue)}
            </span>
          </span>
          <span className="inline-flex items-center gap-1.5 text-muted-foreground">
            <span className="h-2 w-2 rounded-full bg-success" aria-hidden="true" />
            {secondaryLabel}
            <span className="font-mono tabular-nums text-foreground">
              {secondaryValue === null ? METRIC_MISSING_LABEL : formatBytesPerSec(secondaryValue)}
            </span>
          </span>
        </div>
      </div>
      <TrendChart
        className="mt-3"
        height={180}
        times={times}
        formatTime={formatTime}
        formatValue={formatBytesPerSec}
        formatAxis={formatBytesAxis}
        ariaLabel={title}
        series={[
          { key: "primary", label: primaryLabel, color: SERIES_COLOR.rx, values: primaryValues },
          { key: "secondary", label: secondaryLabel, color: SERIES_COLOR.tx, values: secondaryValues },
        ]}
      />
    </Card>
  );
}

interface LoadAvgCardProps {
  applicable?: boolean;
  load1: Nullable;
  load5: Nullable;
  load15: Nullable;
}

function LoadAvgCard({ load1, load5, load15, applicable = true }: LoadAvgCardProps) {
  const { t } = useTranslation();
  const entries: Array<[string, Nullable]> = [
    ["1m", load1],
    ["5m", load5],
    ["15m", load15],
  ];
  return (
    <Card className="p-5">
      <h3 className="text-sm font-semibold text-foreground">{t("monitoring.loadAverage")}</h3>
      {!applicable ? (
        <p className="mt-4 text-sm text-muted-foreground">{t("monitoring.windowsLoadNotApplicable")}</p>
      ) : (
        <div className="mt-4 flex justify-around">
          {entries.map(([label, value]) => (
            <div key={label} className="text-center">
              <p className="text-2xl font-bold tabular-nums text-foreground">
                {value === null ? METRIC_MISSING_LABEL : value.toFixed(2)}
              </p>
              <p className="mt-1 text-[10px] uppercase tracking-wider text-muted-foreground">
                {label}
              </p>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

export function MonitoringDashboard({ serverId, os }: MonitoringDashboardProps) {
  const isWindows = /^windows(?:\s|$)/i.test(os ?? "");
  const { t, lang } = useTranslation();
  const locale = useMemo(() => ({ t, lang }), [t, lang]);
  const [timeRange, setTimeRange] = useState<TimeRange>("1h");
  // The page shares one range, one axis and one statistic: the cards,
  // the charts and the data table describe the same numbers.
  const [axisMode, setAxisMode] = useState<AxisMode>("percent");
  useEffect(() => {
    if (isWindows && axisMode === "load") setAxisMode("percent");
  }, [isWindows, axisMode]);
  const [stat, setStat] = useState<MetricStat>("avg");
  const [showDataTable, setShowDataTable] = useState(false);
  const [tablePage, setTablePage] = useState(1);
  const { data, isLoading, isError, error, isRefetching, refetch } = useMetrics(
    serverId,
    timeRange,
    { stat },
  );

  // Align every column the page can render — all three axes and both statistics,
  // including coverage — because the cards and the network/disk side charts are
  // always visible. Restricting this to the selected axis left them empty when the
  // statistic changed on another axis (R15).
  const alignFields = useMemo(
    () =>
      Array.from(
        new Set<string>([
          ...SERIES_FIELDS,
          ...alignmentFields("percent"),
          ...alignmentFields("throughput"),
          ...alignmentFields("load"),
        ]),
      ),
    [],
  );

  // Axis presentation: a percentage axis is fixed at 100, the others scale to
  // the data and carry their own unit.
  const axisConfig = useMemo(() => {
    switch (axisMode) {
      case "percent":
        return {
          yMax: 100 as number | null,
          formatValue: (value: number) => formatPercent(value, 1),
          formatAxis: (value: number) => `${value.toFixed(0)}%`,
        };
      case "throughput":
        return {
          yMax: null as number | null,
          formatValue: (value: number) => `${formatBytesPerSecondSI(value)}/s`,
          formatAxis: (value: number) => formatBytesPerSecondSI(value),
        };
      default:
        return {
          yMax: null as number | null,
          formatValue: (value: number) => value.toFixed(2),
          formatAxis: (value: number) => value.toFixed(1),
        };
    }
  }, [axisMode]);

  const notice = downsampleNotice(data?.meta);
  const coverage = formatCoverage(data?.meta?.coverage);

  const formatTime = useMemo(() => makeTimeFormatter(timeRange), [timeRange]);
  // `data.points`/`data.window` keep a stable reference between renders, so the
  // alignment below only recomputes when a fetch actually returns new data.
  const points = data?.points;
  const window = data?.window;

  // Rebuild the full bucket grid for the requested window: the backend only
  // returns buckets that hold samples, which would compress gaps when plotted.
  const aligned = useMemo(
    () =>
      window
        ? buildAlignedSeries(points ?? [], window, alignFields)
        : {
            times: [] as string[],
            series: {} as Record<string, Nullable[]>,
            lastSampleTime: null,
          },
    [points, window, alignFields],
  );

  const times = aligned.times;
  // Last collected comes from real samples — never a filled empty bucket.
  const lastCollected = aligned.lastSampleTime;

  const netRecv = aligned.series.net_recv_rate ?? [];
  const netSent = aligned.series.net_sent_rate ?? [];
  const diskRead = aligned.series.disk_read_rate ?? [];
  const diskWrite = aligned.series.disk_write_rate ?? [];
  const load1 = aligned.series.load_1 ?? [];
  const load5 = aligned.series.load_5 ?? [];
  const load15 = aligned.series.load_15 ?? [];
  // The sampled maxima are separate columns; the page-level statistic
  // decides which column every card and side chart reads (R15).
  const netRecvPeak = aligned.series.net_recv_peak ?? [];
  const netSentPeak = aligned.series.net_sent_peak ?? [];
  const diskReadPeak = aligned.series.disk_read_peak ?? [];
  const diskWritePeak = aligned.series.disk_write_peak ?? [];
  const load1Peak = aligned.series.load_1_peak ?? [];
  const load5Peak = aligned.series.load_5_peak ?? [];
  const load15Peak = aligned.series.load_15_peak ?? [];
  const pick = (peak: Nullable[], average: Nullable[]) => (stat === "peak" ? peak : average);
  // Keep each chart's series array stable during table toggles/pagination.
  // The locale object includes the language because `t` itself stays stable.
  const seriesByAxis = useMemo(() => {
    const build = (mode: AxisMode) => {
      const fields = seriesForStat(mode, stat);
      return AXIS_SERIES[mode].map((axis, index) => {
        const pattern = seriesPattern(index);
        return {
          key: axis.label,
          label: locale.t(isWindows && axis.label === "swap" ? "metrics.series.pageFile" : seriesLabelKey(mode, index)),
          color: AXIS_COLOR[axis.label] ?? SERIES_COLOR.cpu,
          dash: pattern.dash,
          marker: pattern.marker as "circle" | "square" | "triangle" | "diamond",
          values: aligned.series[fields[index]] ?? [],
        };
      });
    };
    return { percent: build("percent"), throughput: build("throughput"), load: build("load") };
  }, [aligned, stat, locale, isWindows]);
  const chartSeries = seriesByAxis[axisMode];

  const usageCards = useMemo(() => {
    const definitions = [
      { key: "cpu", title: "monitoring.cpuUsage", sum: "cpu_sum", count: "cpu_count" },
      { key: "memory", title: "monitoring.memoryUsage", sum: "memory_percent_sum", count: "memory_percent_count" },
      { key: "disk", title: "monitoring.diskUsage", sum: "disk_percent_sum", count: "disk_percent_count" },
      { key: "swap", title: isWindows ? "monitoring.pageFileUsage" : "monitoring.swapUsage", sum: "swap_percent_sum", count: "swap_percent_count" },
    ];
    return definitions.map((card, index) => ({
      key: card.key,
      label: locale.t(card.title),
      value: latestOf(seriesByAxis.percent[index].values),
      avg: weightedMean(points ?? [], card.sum, card.count, AXIS_SERIES.percent[index].avgField),
      max: maxOf(aligned.series[AXIS_SERIES.percent[index].peakField] ?? []),
      series: [seriesByAxis.percent[index]],
    }));
  }, [aligned, points, seriesByAxis, isWindows, locale]);

  // Each independent chart describes its own metric, range and statistic.
  const descriptions = useMemo(() => {
    const describe = (title: string, mode: AxisMode) => {
      const built = chartDescription({
        title,
        from: data?.window.from ?? "",
        to: data?.window.to ?? "",
        timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        actualInterval: data?.meta?.actualInterval ?? "",
        stat,
        unit: axisUnit(mode),
        lastSampleAt: aligned.lastSampleTime,
      });
      return locale.t(built.key, built.params);
    };
    return {
      main: describe(locale.t(`metrics.axis.${axisMode}`), axisMode),
      usage: usageCards.map((card) => describe(card.label, "percent")),
    };
  }, [aligned.lastSampleTime, axisMode, data?.meta?.actualInterval, data?.window.from, data?.window.to, stat, usageCards, locale]);

  const caption = useMemo(() => {
    const built = dataTableCaption({
      title: locale.t(`metrics.axis.${axisMode}`),
      from: data?.window.from ?? "",
      to: data?.window.to ?? "",
      interval: data?.meta?.actualInterval ?? "",
      stat,
      unit: axisUnit(axisMode),
    });
    return locale.t(built.key, built.params);
  }, [axisMode, data?.meta?.actualInterval, data?.window.from, data?.window.to, stat, locale]);

  // Only a configuration change (range, axis, statistic) returns the data view to
  // its first page: an automatic refresh must not move the reader (R17). The page
  // is clamped below when the row count shrinks.
  useEffect(() => {
    setTablePage(1);
  }, [axisMode, stat, timeRange]);

  const tableRows = pageSlice(times.length, tablePage);
  useEffect(() => {
    if (tableRows.page !== tablePage) setTablePage(tableRows.page);
  }, [tableRows.page, tablePage]);
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold text-foreground">{t("monitoring.title")}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {lastCollected
              ? t("monitoring.lastCollected", { time: formatTime(lastCollected) })
              : t("monitoring.neverCollected")}
          </p>
        </div>
        <TimeRangeSelector selected={timeRange} onChange={setTimeRange} />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div
          role="group"
          aria-label={t("metrics.axisLabel")}
          className="inline-flex rounded-lg border border-border bg-muted/60 p-0.5"
        >
          {(["percent", "throughput", "load"] as const).map((mode) => (
            <button
              key={mode}
              type="button"
              disabled={isWindows && mode === "load"}
              title={isWindows && mode === "load" ? t("monitoring.windowsLoadNotApplicable") : undefined}
              aria-pressed={axisMode === mode}
              onClick={() => setAxisMode(mode)}
              className={cn(
                "touch-target rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                axisMode === mode
                  ? "bg-primary text-primary-foreground shadow-sm"
                  : "text-muted-foreground hover:bg-secondary hover:text-foreground",
              )}
            >
              {t(`metrics.axis.${mode}`)}
            </button>
          ))}
        </div>

        <div
          role="group"
          aria-label={t("metrics.statLabel")}
          className="inline-flex rounded-lg border border-border bg-muted/60 p-0.5"
        >
          {(["avg", "peak"] as const).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={stat === value}
              onClick={() => setStat(value)}
              className={cn(
                "touch-target rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                stat === value
                  ? "bg-primary text-primary-foreground shadow-sm"
                  : "text-muted-foreground hover:bg-secondary hover:text-foreground",
              )}
            >
              {t(`metrics.stat.${value}`)}
            </button>
          ))}
        </div>

        <button
          type="button"
          aria-expanded={showDataTable}
          aria-controls="monitoring-data-table"
          onClick={() => setShowDataTable((open) => !open)}
          className="touch-target rounded-lg border border-border bg-muted/60 px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          {showDataTable ? t("metrics.hideDataTable") : t("metrics.showDataTable")}
        </button>
      </div>

      {isLoading && <LoadingSkeleton />}

      {isError && (
        <ErrorState
          message={translateApiError(error, t, t("monitoring.loadError"))}
          onRetry={() => refetch()}
        />
      )}

      {data && (points?.length ?? 0) === 0 && !isLoading && !isError && (
        <EmptyState message={t("monitoring.emptyState")} />
      )}

      {data && (points?.length ?? 0) > 0 && (
        <>
          <div className={cn("grid gap-4", axisMode === "percent" ? "grid-cols-1 xl:grid-cols-2" : "grid-cols-2 lg:grid-cols-4")}>
            {usageCards.map((card, index) => (
              <MetricStatCard key={card.key} label={card.label} value={card.value} avg={card.avg} max={card.max}>
                {axisMode === "percent" && (
                  <TrendChart
                    className="mt-3"
                    height={200}
                    times={times}
                    yMax={100}
                    formatTime={formatTime}
                    formatValue={axisConfig.formatValue}
                    formatAxis={axisConfig.formatAxis}
                    ariaLabel={card.label}
                    description={descriptions.usage[index]}
                    series={card.series}
                  />
                )}
              </MetricStatCard>
            ))}
          </div>

          {axisMode !== "percent" && <Card className="p-5">
            <h3 className="text-sm font-semibold text-foreground">
              {t(`metrics.axis.${axisMode}`)}
            </h3>
            <TrendChart
              className="mt-3"
              height={240}
              times={times}
              yMax={axisConfig.yMax}
              formatTime={formatTime}
              formatValue={axisConfig.formatValue}
              formatAxis={axisConfig.formatAxis}
              ariaLabel={t(`metrics.axis.${axisMode}`)}
              description={descriptions.main}
              series={chartSeries}
            />
          </Card>}

          {(notice || coverage) && (
            <p role="status" className="text-xs text-muted-foreground">
              {notice ? t(notice.key, notice.params) : null}
              {notice && coverage ? " · " : null}
              {coverage ? t("metrics.coverage", { percent: coverage }) : null}
            </p>
          )}

          {showDataTable && (
            <Card className="p-5">
              <div id="monitoring-data-table" className="overflow-x-auto">
                <table className="w-full text-xs">
                  <caption className="pb-2 text-left text-muted-foreground">{caption}</caption>
                  <thead>
                    <tr className="text-left text-muted-foreground">
                      <th scope="col" className="py-1 pr-3 font-medium">
                        {t("metrics.time")}
                      </th>
                      {chartSeries.map((series) => (
                        <th key={series.key} scope="col" className="py-1 pr-3 font-medium">
                          {series.label}
                        </th>
                      ))}
                      <th scope="col" className="py-1 pr-3 font-medium">
                        {t("metrics.dataTable.coverage")}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {times.slice(tableRows.start, tableRows.end).map((time, offset) => {
                      const index = tableRows.start + offset;
                      const rowValues = chartSeries.map((series) => series.values[index] ?? null);
                      const coverages = AXIS_SERIES[axisMode]
                        .map((axis) =>
                          axis.coverageField ? (aligned.series[axis.coverageField]?.[index] ?? null) : null,
                        )
                        .filter((value): value is number => value !== null);
                      const coverage = coverageCell(
                        axisMode,
                        rowValues,
                        coverages.length > 0 ? Math.max(...coverages) : null,
                        window?.intervalMs ?? 0,
                      );
                      return (
                        <tr key={time} className="border-t border-border/60">
                          <th scope="row" className="py-1 pr-3 font-normal text-muted-foreground">
                            {formatTime(time)}
                          </th>
                          {chartSeries.map((series, seriesIndex) => {
                            const value = rowValues[seriesIndex];
                            return (
                              <td key={series.key} className="py-1 pr-3 font-mono tabular-nums">
                                {value === null || value === undefined || !Number.isFinite(value)
                                  ? METRIC_MISSING_LABEL
                                  : axisConfig.formatValue(value)}
                              </td>
                            );
                          })}
                          <td className="py-1 pr-3 text-muted-foreground">
                            {t(coverage.key, coverage.params)}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>

              <div className="mt-3 flex items-center justify-between gap-3 text-xs text-muted-foreground">
                <span aria-live="polite">
                  {t("metrics.dataTable.page", { page: tableRows.page, pages: tableRows.pages })}
                </span>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => setTablePage((page) => Math.max(1, page - 1))}
                    disabled={tableRows.page <= 1}
                    className="touch-target rounded-md border border-border px-3 py-1.5 font-medium transition-colors hover:bg-secondary disabled:opacity-50"
                  >
                    {t("metrics.dataTable.prev")}
                  </button>
                  <button
                    type="button"
                    onClick={() => setTablePage((page) => Math.min(tableRows.pages, page + 1))}
                    disabled={tableRows.page >= tableRows.pages}
                    className="touch-target rounded-md border border-border px-3 py-1.5 font-medium transition-colors hover:bg-secondary disabled:opacity-50"
                  >
                    {t("metrics.dataTable.next")}
                  </button>
                </div>
              </div>
            </Card>
          )}

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <ThroughputCard
              title={t("monitoring.network")}
              times={times}
              formatTime={formatTime}
              primaryLabel={t("monitoring.rx")}
              primaryValue={latestOf(pick(netRecvPeak, netRecv))}
              primaryValues={pick(netRecvPeak, netRecv)}
              secondaryLabel={t("monitoring.tx")}
              secondaryValue={latestOf(pick(netSentPeak, netSent))}
              secondaryValues={pick(netSentPeak, netSent)}
            />
            <ThroughputCard
              title={t("monitoring.diskIo")}
              times={times}
              formatTime={formatTime}
              primaryLabel={t("monitoring.read")}
              primaryValue={latestOf(pick(diskReadPeak, diskRead))}
              primaryValues={pick(diskReadPeak, diskRead)}
              secondaryLabel={t("monitoring.write")}
              secondaryValue={latestOf(pick(diskWritePeak, diskWrite))}
              secondaryValues={pick(diskWritePeak, diskWrite)}
            />
          </div>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <LoadAvgCard
              applicable={!isWindows}
              load1={latestOf(pick(load1Peak, load1))}
              load5={latestOf(pick(load5Peak, load5))}
              load15={latestOf(pick(load15Peak, load15))}
            />
          </div>

          <p className="text-xs text-muted-foreground">{t("monitoring.gapNote")}</p>
        </>
      )}

      {isRefetching && (
        <p className="text-right text-xs text-muted-foreground">{t("common.refreshing")}</p>
      )}
    </div>
  );
}
