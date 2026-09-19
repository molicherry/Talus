import { Loader2 } from "lucide-react";
import { useTranslation } from "../../../i18n";
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
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (isError || !server) {
    return (
      <div className="rounded-2xl border border-danger/30 bg-danger-subtle p-6 text-center">
        <p className="text-sm text-danger">
          {error instanceof Error ? error.message : t("server.notFound")}
        </p>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex-shrink-0 px-1 py-2">
        <h1 className="text-xl font-semibold text-foreground">{t("terminal.title", { name: server.name })}</h1>
        <p className="text-xs text-muted-foreground">
          {server.host}:{server.port}
        </p>
      </div>
      <div className="mt-2 flex-1 overflow-hidden rounded-2xl border border-border bg-[#0d1117]">
        <TerminalView serverId={serverId} />
      </div>
    </div>
  );
}
