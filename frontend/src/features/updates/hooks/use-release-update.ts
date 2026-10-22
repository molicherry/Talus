import { usePageVisibility } from "../../../hooks/use-page-visibility";
import { useQuery } from "../../../lib/query";
import { getUpdateStatus } from "../api";

export function useReleaseUpdate() {
  const visible = usePageVisibility();
  return useQuery({
    queryKey: ["release-update"],
    queryFn: getUpdateStatus,
    enabled: visible,
    staleTime: 5 * 60 * 1000,
    refetchInterval: visible ? 5 * 60 * 1000 : false,
    retry: 0,
  });
}
