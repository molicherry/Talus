import { useState } from "react";
import { FilePanel } from "../../files/components/file-panel";
import { Loader2 } from "lucide-react";
import { translateApiError } from "../../../lib/api-error";
import { useTranslation } from "../../../i18n";
import { Link, useParams } from "react-router-dom";
import { useServer } from "../../servers/hooks/use-servers";
import { HostKeyWarning } from "../../servers/components/host-key-warning";
import { TerminalView } from "./terminal";

export function TerminalPage() {
  const { id } = useParams<{ id: string }>();
  const serverId = id ? Number(id) : 0;
  const { data: server, isLoading, isError, error } = useServer(serverId);
  const jumpId = server?.connection?.jump_server_id ?? 0;
  const jump = useServer(jumpId);
  const { t } = useTranslation();
  const [filesOpen, setFilesOpen] = useState(false);

  if (isLoading || (jumpId && jump.isLoading)) {
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

  if (jumpId && (jump.isError || !jump.data)) {
    return <p role="alert" className="text-danger">{translateApiError(jump.error, t, t("server.notFound"))}</p>;
  }
  const needsConfirmation = (candidate: typeof server) => candidate.host_key_unverified || candidate.host_key_mismatch;
  const blocker = jump.data && needsConfirmation(jump.data) ? jump.data : needsConfirmation(server) ? server : null;
  if (blocker) {
    return (
      <div className="mx-auto max-w-3xl py-4">
        <HostKeyWarning key={blocker.id} server={blocker} autoDiscover />
        <Link className="text-sm text-link hover:underline" to={`/servers/${serverId}`}>
          {t("server.hostKey.cancel")}
        </Link>
      </div>
    );
  }

  // The terminal toolbar is the single header (REQ-09 "信息与状态"): it carries the
  // server name, the address, the connection state and the controls, so the page
  // adds no duplicate title above it.
  return (
    <div className="ssh-terminal-page flex min-h-0 flex-col">
      <h1 className="sr-only">{t("terminal.title", { name: server.name })}</h1>
      <div className="ssh-files-layout" data-files-open={filesOpen}>
       <div className="ssh-terminal-pane">
        <TerminalView
          serverId={serverId}
          serverName={server.name}
          host={server.host}
          port={server.port}
          filesOpen={filesOpen}
          onToggleFiles={() => setFilesOpen(open => !open)}
        />
       </div>
       {filesOpen && <FilePanel key={serverId} serverId={serverId} onClose={() => setFilesOpen(false)} />}
      </div>
    </div>
  );
}
