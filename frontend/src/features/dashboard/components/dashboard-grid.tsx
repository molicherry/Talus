import { RefreshError } from "../../../components/ui/refresh-error";
import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { useTranslation } from "../../../i18n";
import type { Server } from "../../../types/models";
import { DashboardEmpty } from "./dashboard-empty";
import { DashboardSummary } from "./dashboard-summary";
import { ServerCard } from "./server-card";
import { ServerCardSkeleton } from "./server-card-skeleton";

interface DashboardGridProps {
  servers: Server[] | undefined;
  isLoading: boolean;
  isError: boolean;
  isFetching: boolean;
  error: Error | null;
  refetch: () => void;
}

export function DashboardGrid({ servers, isLoading, isError, isFetching, error, refetch }: DashboardGridProps) {
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <>
        <div className="mb-8 grid grid-cols-1 gap-4 sm:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <div key={i} className="h-[68px] animate-pulse rounded-2xl border border-border bg-card" />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
          <ServerCardSkeleton />
          <ServerCardSkeleton />
          <ServerCardSkeleton />
        </div>
      </>
    );
  }

  if (isError && servers === undefined) {
    return (
      <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
        <p className="text-sm text-danger">
          {translateApiError(error, t, t("server.loadError"))}
        </p>
        <Button type="button" variant="outline" onClick={() => refetch()} className="mt-4">
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  const refreshError = <RefreshError error={error} isFetching={isFetching} onRetry={refetch} />;

  if (!servers || servers.length === 0) {
    return <>{refreshError}<DashboardEmpty /></>;
  }

  return (
    <>
      {refreshError}
      <DashboardSummary servers={servers} />
      <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
        {servers.map((server) => (
          <ServerCard key={server.id} server={server} />
        ))}
      </div>
    </>
  );
}
