import { Loader2 } from "lucide-react";
import { translateApiError } from "../../../lib/api-error";
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
          {translateApiError(error, t, t("server.notFound"))}
        </p>
      </div>
    );
  }

  // The terminal toolbar is the single header (REQ-09 "信息与状态"): it carries the
  // server name, the address, the connection state and the controls, so the page
  // adds no duplicate title above it.
  return (
    <div className="flex h-full min-h-0 flex-col overflow-x-hidden">
      <h1 className="sr-only">{t("terminal.title", { name: server.name })}</h1>
      <div className="min-h-0 flex-1 overflow-hidden rounded-2xl border border-border bg-[#0d1117]">
        <TerminalView
          serverId={serverId}
          serverName={server.name}
          host={server.host}
          port={server.port}
        />
      </div>
    </div>
  );
}
