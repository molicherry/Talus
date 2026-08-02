import { useQuery } from "@tanstack/react-query";
import { usePageVisibility } from "../../../hooks/use-page-visibility";
import type { TimeRange } from "../../../types/metrics";
import { getMetrics } from "../api";

export function useMetrics(serverId: number, timeRange: TimeRange) {
  const isVisible = usePageVisibility();

  return useQuery({
    queryKey: ["metrics", serverId, timeRange],
    queryFn: () => getMetrics(serverId, timeRange),
    staleTime: 5_000,
    refetchInterval: isVisible ? 60_000 : false,
  });
}
