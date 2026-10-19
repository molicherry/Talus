import { useQuery } from "../../../lib/query";
import { getServersWithMetrics } from "../api";

export function useDashboardData() {
  return useQuery({
    queryKey: ["dashboard"],
    queryFn: getServersWithMetrics,
    refetchInterval: 5_000,
    staleTime: 3_000,
  });
}
