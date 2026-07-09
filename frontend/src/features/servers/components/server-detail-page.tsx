import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router-dom";
import { MonitoringDashboard } from "../../monitoring/components/monitoring-dashboard";
import { useServer } from "../hooks/use-servers";
import { ServerInfoBar } from "./server-info-bar";

export function ServerDetailPage() {
  const { id } = useParams<{ id: string }>();
  const serverId = id ? Number(id) : 0;
  const { data: server, isLoading, isError, error } = useServer(serverId);
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-6 w-6 animate-spin text-gray-500 dark:text-gray-400" />
      </div>
    );
  }

  if (isError || !server) {
    return (
      <div className="rounded-lg border border-red-300 bg-red-50 p-6 text-center dark:border-red-800 dark:bg-red-900/20">
        <p className="text-sm text-red-600 dark:text-red-400">
          {error instanceof Error ? error.message : t("server.notFound")}
        </p>
      </div>
    );
  }

  return (
    <div>
      <ServerInfoBar server={server} />
      <MonitoringDashboard serverId={serverId} />
    </div>
  );
}
