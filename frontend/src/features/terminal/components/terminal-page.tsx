import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useParams } from "react-router-dom";
import { useServer } from "../../servers/hooks/use-servers";
import { TerminalView } from "./terminal";

export function TerminalPage() {
  const { id } = useParams<{ id: string }>();
  const serverId = id ? Number(id) : 0;
  const { data: server, isLoading, isError, error } = useServer(serverId);
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center py-12">
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
    <div className="flex h-full flex-col">
      <div className="flex-shrink-0 px-1 py-2">
        <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100">{t("terminal.title", { name: server.name })}</h1>
        <p className="text-xs text-gray-400 dark:text-gray-500">
          {server.host}:{server.port}
        </p>
      </div>
      <div className="mt-2 flex-1 overflow-hidden rounded-lg border border-gray-200 bg-[#0d1117] dark:border-gray-800">
        <TerminalView serverId={serverId} />
      </div>
    </div>
  );
}
