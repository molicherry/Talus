import { useQuery } from "../../../lib/query";
import { usePageVisibility } from "../../../hooks/use-page-visibility";
import type { MetricStat, TimeRange } from "../../../types/metric-axes";
import { getMetrics } from "../api";

export interface UseMetricsOptions {
  stat?: MetricStat;
}

export function useMetrics(
  serverId: number,
  timeRange: TimeRange,
  options: UseMetricsOptions = {},
) {
  const isVisible = usePageVisibility();
  const stat = options.stat ?? "avg";

  return useQuery({
    // The statistic is part of the key: a late response for another statistic
    // is isolated instead of replacing the rendered series.
    queryKey: ["metrics", serverId, timeRange, stat],
    queryFn: () => getMetrics(serverId, timeRange, { stat }),
    staleTime: 5_000,
    refetchInterval: isVisible ? 60_000 : false,
  });
}
