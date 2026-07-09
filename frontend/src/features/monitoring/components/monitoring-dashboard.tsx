import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { MetricPoint, TimeRange } from "../../../types/metrics";
import { useMetrics } from "../hooks/use-metrics";
import { EmptyState } from "./empty-state";
import { ErrorState } from "./error-state";
import { GaugeChart } from "./gauge-chart";
import { LoadingSkeleton } from "./loading-skeleton";
import { Sparkline } from "./sparkline";
import { TimeRangeSelector } from "./time-range-selector";

interface MonitoringDashboardProps {
  serverId: number;
}

function extractSparklineData(
  data: MetricPoint[],
  field: keyof MetricPoint,
): Array<{ value: number }> {
  return data.map((p) => ({ value: (p[field] as number) ?? 0 }));
}

function getLatest(data: MetricPoint[], field: keyof MetricPoint): number {
  const last = data[data.length - 1];
  return (last?.[field] as number) ?? 0;
}

function getAvg(data: MetricPoint[], field: keyof MetricPoint): number {
  if (data.length === 0) return 0;
  const sum = data.reduce((acc, p) => acc + ((p[field] as number) ?? 0), 0);
  return sum / data.length;
}

function getMax(data: MetricPoint[], field: keyof MetricPoint): number {
  if (data.length === 0) return 0;
  return Math.max(...data.map((p) => (p[field] as number) ?? 0));
}

function getGaugeColor(value: number): string {
  if (value < 60) return "#22c55e";
  if (value < 80) return "#f59e0b";
  return "#ef4444";
}

function formatBytesPerSec(bytesPerSec: number): string {
  if (bytesPerSec >= 1_000_000) return `${(bytesPerSec / 1_000_000).toFixed(1)} MB/s`;
  if (bytesPerSec >= 1_000) return `${(bytesPerSec / 1_000).toFixed(1)} KB/s`;
  return `${bytesPerSec.toFixed(1)} B/s`;
}

interface MetricCardProps {
  title: string;
  value: number;
  sparklineData: Array<{ value: number }>;
  avg: number;
  max: number;
}

function MetricCard({ title, value, sparklineData, avg, max }: MetricCardProps) {
  const color = getGaugeColor(value);

  return (
    <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm transition-shadow hover:shadow-md dark:border-gray-800 dark:bg-gray-900">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400">
        {title}
      </h3>
      <div className="mt-4 flex justify-center">
        <GaugeChart value={value} />
      </div>
      <div className="mt-3">
        <Sparkline color={color} data={sparklineData} />
      </div>
      <div className="mt-3 flex items-center justify-between border-t border-gray-100 pt-3 dark:border-gray-800">
        <div className="text-center">
          <p className="text-[10px] uppercase tracking-wider text-gray-400 dark:text-gray-500">
            Avg
          </p>
          <p className="mt-0.5 text-sm font-medium tabular-nums text-gray-700 dark:text-gray-300">
            {avg.toFixed(1)}%
          </p>
        </div>
        <div className="text-center">
          <p className="text-[10px] uppercase tracking-wider text-gray-400 dark:text-gray-500">
            Max
          </p>
          <p className="mt-0.5 text-sm font-medium tabular-nums text-gray-700 dark:text-gray-300">
            {max.toFixed(1)}%
          </p>
        </div>
      </div>
    </div>
  );
}

interface LoadAvgCardProps {
  load1: number;
  load5: number;
  load15: number;
}

function LoadAvgCard({ load1, load5, load15 }: LoadAvgCardProps) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-800 dark:bg-gray-900">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400">
        Load Average
      </h3>
      <div className="mt-4 flex justify-around">
        {[["1m", load1], ["5m", load5], ["15m", load15]].map(([label, val]) => (
          <div key={label} className="text-center">
            <p className="text-2xl font-bold tabular-nums text-gray-900 dark:text-gray-100">
              {(val as number).toFixed(2)}
            </p>
            <p className="mt-1 text-[10px] uppercase tracking-wider text-gray-400 dark:text-gray-500">
              {label}
            </p>
          </div>
        ))}
      </div>
    </div>
  );
}

interface RateCardProps {
  title: string;
  recvRate: number;
  sentRate: number;
  recvSparkline: Array<{ value: number }>;
  sentSparkline: Array<{ value: number }>;
}

function RateCard({ title, recvRate, sentRate, recvSparkline, sentSparkline }: RateCardProps) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-800 dark:bg-gray-900">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-gray-400">
        {title}
      </h3>
      <div className="mt-4 space-y-3">
        <div>
          <div className="flex items-center justify-between text-sm">
            <span className="text-gray-500 dark:text-gray-400">RX</span>
            <span className="font-mono tabular-nums font-medium text-blue-600 dark:text-blue-400">
              {formatBytesPerSec(recvRate)}
            </span>
          </div>
          <div className="mt-1">
            <Sparkline color="#3b82f6" data={recvSparkline} />
          </div>
        </div>
        <div>
          <div className="flex items-center justify-between text-sm">
            <span className="text-gray-500 dark:text-gray-400">TX</span>
            <span className="font-mono tabular-nums font-medium text-green-600 dark:text-green-400">
              {formatBytesPerSec(sentRate)}
            </span>
          </div>
          <div className="mt-1">
            <Sparkline color="#22c55e" data={sentSparkline} />
          </div>
        </div>
      </div>
    </div>
  );
}

export function MonitoringDashboard({ serverId }: MonitoringDashboardProps) {
  const { t } = useTranslation();
  const [timeRange, setTimeRange] = useState<TimeRange>("1h");
  const { data, isLoading, isError, error, isRefetching, refetch } = useMetrics(
    serverId,
    timeRange,
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-100">
          {t("monitoring.title")}
        </h2>
        <TimeRangeSelector selected={timeRange} onChange={setTimeRange} />
      </div>

      {isLoading && <LoadingSkeleton />}

      {isError && (
        <ErrorState
          message={error instanceof Error ? error.message : t("monitoring.loadError")}
          onRetry={() => refetch()}
        />
      )}

      {data && data.length === 0 && !isLoading && !isError && (
        <EmptyState message={t("monitoring.emptyState")} />
      )}

      {data && data.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <MetricCard
            title={t("monitoring.cpuUsage")}
            value={getLatest(data, "cpu_percent")}
            sparklineData={extractSparklineData(data, "cpu_percent")}
            avg={getAvg(data, "cpu_percent")}
            max={getMax(data, "cpu_percent")}
          />
          <MetricCard
            title={t("monitoring.memoryUsage")}
            value={getLatest(data, "memory_percent")}
            sparklineData={extractSparklineData(data, "memory_percent")}
            avg={getAvg(data, "memory_percent")}
            max={getMax(data, "memory_percent")}
          />
          <MetricCard
            title={t("monitoring.diskUsage")}
            value={getLatest(data, "disk_percent")}
            sparklineData={extractSparklineData(data, "disk_percent")}
            avg={getAvg(data, "disk_percent")}
            max={getMax(data, "disk_percent")}
          />
          <LoadAvgCard
            load1={getLatest(data, "load_1")}
            load5={getLatest(data, "load_5")}
            load15={getLatest(data, "load_15")}
          />
          <MetricCard
            title="Swap"
            value={getLatest(data, "swap_percent")}
            sparklineData={extractSparklineData(data, "swap_percent")}
            avg={getAvg(data, "swap_percent")}
            max={getMax(data, "swap_percent")}
          />
          <RateCard
            title="Network"
            recvRate={getLatest(data, "net_recv_rate")}
            sentRate={getLatest(data, "net_sent_rate")}
            recvSparkline={extractSparklineData(data, "net_recv_rate")}
            sentSparkline={extractSparklineData(data, "net_sent_rate")}
          />
          <RateCard
            title="Disk I/O"
            recvRate={getLatest(data, "disk_read_rate")}
            sentRate={getLatest(data, "disk_write_rate")}
            recvSparkline={extractSparklineData(data, "disk_read_rate")}
            sentSparkline={extractSparklineData(data, "disk_write_rate")}
          />
        </div>
      )}

      {isRefetching && (
        <p className="text-right text-xs text-gray-400 dark:text-gray-600">
          {t("common.refreshing")}
        </p>
      )}
    </div>
  );
}
