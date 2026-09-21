import { Loader2 } from "lucide-react";
import { translateApiError } from "../../../lib/api-error";
import { useTranslation } from "../../../i18n";
import { useParams } from "react-router-dom";
import { useServer } from "../hooks/use-servers";
import { ServerForm } from "./server-form";

export function ServerEditPage() {
  const { id } = useParams<{ id: string }>();
  const serverId = id ? Number(id) : 0;
  const { data: server, isLoading, isError, error } = useServer(serverId);
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
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

  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("server.edit")}</h1>
      <ServerForm server={server} />
    </div>
  );
}
