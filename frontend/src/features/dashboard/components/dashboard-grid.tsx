import { useTranslation } from "react-i18next";
import type { Server } from "../../../types/models";
import { Button } from "../../../components/ui/button";
import { DashboardEmpty } from "./dashboard-empty";
import { ServerCard } from "./server-card";
import { ServerCardSkeleton } from "./server-card-skeleton";

interface DashboardGridProps {
  servers: Server[] | undefined;
  isLoading: boolean;
  isError: boolean;
  error: Error | null;
  refetch: () => void;
}

export function DashboardGrid({ servers, isLoading, isError, error, refetch }: DashboardGridProps) {
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
        <ServerCardSkeleton />
        <ServerCardSkeleton />
        <ServerCardSkeleton />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
        <p className="text-sm text-danger">
          {error instanceof Error ? error.message : t("server.loadError")}
        </p>
        <Button type="button" variant="outline" onClick={() => refetch()} className="mt-4">
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  if (!servers || servers.length === 0) {
    return <DashboardEmpty />;
  }

  return (
    <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
      {servers.map((server) => (
        <ServerCard key={server.id} server={server} />
      ))}
    </div>
  );
}
