import { useTranslation } from "react-i18next";
import type { Server } from "../../../types/models";
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
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <ServerCardSkeleton />
        <ServerCardSkeleton />
        <ServerCardSkeleton />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-lg border border-red-300 bg-red-50 p-6 text-center dark:border-red-800 dark:bg-red-900/20">
        <p className="text-sm text-red-600 dark:text-red-400">
          {error instanceof Error ? error.message : t("server.loadError")}
        </p>
        <button
          type="button"
          onClick={() => refetch()}
          className="mt-3 rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
        >
          {t("common.retry")}
        </button>
      </div>
    );
  }

  if (!servers || servers.length === 0) {
    return <DashboardEmpty />;
  }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {servers.map((server) => (
        <ServerCard key={server.id} server={server} />
      ))}
    </div>
  );
}
