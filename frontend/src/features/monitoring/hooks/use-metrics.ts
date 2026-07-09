import { useQuery } from "@tanstack/react-query";
import type { TimeRange } from "../../../types/metrics";
import { getMetrics } from "../api";

export function useMetrics(serverId: number, timeRange: TimeRange) {
  return useQuery({
    queryKey: ["metrics", serverId, timeRange],
    queryFn: () => getMetrics(serverId, timeRange),
    staleTime: 5_000,
    refetchInterval: 60_000,
  });
}
