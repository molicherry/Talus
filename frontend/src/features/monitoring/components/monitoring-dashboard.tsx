import { useMemo, useState } from "react";

import { Card } from "../../../components/ui/card";
import { useTranslation } from "../../../i18n";
import {
  formatPercent,
  METRIC_MISSING_LABEL,
  metricLevelFill,
  metricLevelText,
  percentLevel,
} from "../../../lib/metric-level";
import { cn } from "../../../lib/utils";
import type { TimeRange } from "../../../types/metrics";
import { useMetrics } from "../hooks/use-metrics";
import { buildAlignedSeries } from "../lib/series";
import { EmptyState } from "./empty-state";
import { ErrorState } from "./error-state";
import { LoadingSkeleton } from "./loading-skeleton";
import { TimeRangeSelector } from "./time-range-selector";
import { TrendChart } from "./trend-chart";

interface MonitoringDashboardProps {
  serverId: number;
}

/**
 * A metric series keeps `null` for buckets without a sample instead of
 * coercing them to 0. Treating "not collected" as 0 previously rendered empty
 * buckets as "almost idle" and dragged averages down.
 */
type Nullable = number | null;

const SERIES_COLOR = {
  cpu: "var(--color-primary)",
  memory: "var(--color-success)",
  disk: "var(--color-warning)",
  rx: "var(--color-primary)",
  tx: "var(--color-success)",
  read: "var(--color-primary)",
  write: "var(--color-success)",
} as const;

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

/** Average over collected samples only — missing buckets are excluded. */
function avgOf(values: Nullable[]): number | null {
  const present = values.filter((v): v is number => v !== null);
  if (present.length === 0) return null;
  return present.reduce((acc, v) => acc + v, 0) / present.length;
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
    if (range === "7d") return `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
  };
}

interface MetricStatProps {
  label: string;
  value: Nullable;
  avg: Nullable;
  max: Nullable;
}

function MetricStat({ label, value, avg, max }: MetricStatProps) {
  const { t } = useTranslation();
  const level = percentLevel(value);
  return (
    <Card className="p-4">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </span>
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
  load1: Nullable;
  load5: Nullable;
  load15: Nullable;
}

function LoadAvgCard({ load1, load5, load15 }: LoadAvgCardProps) {
  const { t } = useTranslation();
  const entries: Array<[string, Nullable]> = [
    ["1m", load1],
    ["5m", load5],
    ["15m", load15],
  ];
  return (
    <Card className="p-5">
      <h3 className="text-sm font-semibold text-foreground">{t("monitoring.loadAverage")}</h3>
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
    </Card>
  );
}

export function MonitoringDashboard({ serverId }: MonitoringDashboardProps) {
  const { t } = useTranslation();
  const [timeRange, setTimeRange] = useState<TimeRange>("1h");
  const { data, isLoading, isError, error, isRefetching, refetch } = useMetrics(
    serverId,
    timeRange,
  );

  const formatTime = makeTimeFormatter(timeRange);
  // `data.points`/`data.window` keep a stable reference between renders, so the
  // alignment below only recomputes when a fetch actually returns new data.
  const points = data?.points;
  const window = data?.window;

  // Rebuild the full bucket grid for the requested window: the backend only
  // returns buckets that hold samples, which would compress gaps when plotted.
  const aligned = useMemo(
    () =>
      window
        ? buildAlignedSeries(points ?? [], window, SERIES_FIELDS)
        : {
            times: [] as string[],
            series: {} as Record<string, Nullable[]>,
            lastSampleTime: null,
          },
    [points, window],
  );

  const times = aligned.times;
  // Last collected comes from real samples — never a filled empty bucket.
  const lastCollected = aligned.lastSampleTime;

  const cpu = aligned.series.cpu_percent ?? [];
  const memory = aligned.series.memory_percent ?? [];
  const disk = aligned.series.disk_percent ?? [];
  const swap = aligned.series.swap_percent ?? [];
  const netRecv = aligned.series.net_recv_rate ?? [];
  const netSent = aligned.series.net_sent_rate ?? [];
  const diskRead = aligned.series.disk_read_rate ?? [];
  const diskWrite = aligned.series.disk_write_rate ?? [];
  const load1 = aligned.series.load_1 ?? [];
  const load5 = aligned.series.load_5 ?? [];
  const load15 = aligned.series.load_15 ?? [];
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

      {isLoading && <LoadingSkeleton />}

      {isError && (
        <ErrorState
          message={error instanceof Error ? error.message : t("monitoring.loadError")}
          onRetry={() => refetch()}
        />
      )}

      {data && (points?.length ?? 0) === 0 && !isLoading && !isError && (
        <EmptyState message={t("monitoring.emptyState")} />
      )}

      {data && (points?.length ?? 0) > 0 && (
        <>
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <MetricStat
              label={t("monitoring.cpuUsage")}
              value={latestOf(cpu)}
              avg={avgOf(cpu)}
              max={maxOf(cpu)}
            />
            <MetricStat
              label={t("monitoring.memoryUsage")}
              value={latestOf(memory)}
              avg={avgOf(memory)}
              max={maxOf(memory)}
            />
            <MetricStat
              label={t("monitoring.diskUsage")}
              value={latestOf(disk)}
              avg={avgOf(disk)}
              max={maxOf(disk)}
            />
            <MetricStat
              label={t("monitoring.swapUsage")}
              value={latestOf(swap)}
              avg={avgOf(swap)}
              max={maxOf(swap)}
            />
          </div>

          <Card className="p-5">
            <h3 className="text-sm font-semibold text-foreground">{t("monitoring.usageTrend")}</h3>
            <TrendChart
              className="mt-3"
              height={240}
              times={times}
              yMax={100}
              formatTime={formatTime}
              formatValue={(v) => formatPercent(v, 1)}
              formatAxis={(v) => `${v.toFixed(0)}%`}
              ariaLabel={t("monitoring.usageTrend")}
              series={[
                { key: "cpu", label: t("monitoring.cpuUsage"), color: SERIES_COLOR.cpu, values: cpu },
                {
                  key: "memory",
                  label: t("monitoring.memoryUsage"),
                  color: SERIES_COLOR.memory,
                  values: memory,
                },
                { key: "disk", label: t("monitoring.diskUsage"), color: SERIES_COLOR.disk, values: disk },
              ]}
            />
          </Card>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <ThroughputCard
              title={t("monitoring.network")}
              times={times}
              formatTime={formatTime}
              primaryLabel={t("monitoring.rx")}
              primaryValue={latestOf(netRecv)}
              primaryValues={netRecv}
              secondaryLabel={t("monitoring.tx")}
              secondaryValue={latestOf(netSent)}
              secondaryValues={netSent}
            />
            <ThroughputCard
              title={t("monitoring.diskIo")}
              times={times}
              formatTime={formatTime}
              primaryLabel={t("monitoring.read")}
              primaryValue={latestOf(diskRead)}
              primaryValues={diskRead}
              secondaryLabel={t("monitoring.write")}
              secondaryValue={latestOf(diskWrite)}
              secondaryValues={diskWrite}
            />
          </div>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <LoadAvgCard
              load1={latestOf(load1)}
              load5={latestOf(load5)}
              load15={latestOf(load15)}
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
