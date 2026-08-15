import { useQuery } from "../../../lib/query";
import { getServersWithMetrics } from "../api";

export function useDashboardData() {
  return useQuery({
    queryKey: ["dashboard"],
    queryFn: getServersWithMetrics,
    refetchInterval: 60_000,
    staleTime: 30_000,
  });
}
