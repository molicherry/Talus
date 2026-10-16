import { Loader2 } from "lucide-react";
import { useState } from "react";
import { Button } from "../../../components/ui/button";
import { useAuth } from "../../../hooks/use-auth";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { translateApiError } from "../../../lib/api-error";
import { useTestServerConnection } from "../hooks/use-servers";

export function ConnectionTestButton({ serverID, unsaved = false, disabled = false }: { serverID: number; unsaved?: boolean; disabled?: boolean }) {
  const { isAdmin } = useAuth();
  const { t } = useTranslation();
  const mutation = useTestServerConnection();
  const [result, setResult] = useState<{ success: boolean; message: string } | null>(null);
  if (!isAdmin) return null;
  const run = () => {
    setResult(null);
    mutation.mutate(serverID, {
      onSuccess: (response) => setResult({ success: true, message: t("server.connection.testSuccess", { latency: response.latency_ms }) }),
      onError: (error) => {
        const stage = error instanceof ApiClientError ? (error.params?.connection_stage ?? error.params?.stage) : undefined;
        const stageKey = `server.connection.stages.${stage}`;
        const stages = ["proxy_connect", "proxy_auth", "proxy_connect_request", "jump_connect", "jump_auth",
          "jump_forward", "target_connect", "target_auth", "target_host_key", "jump_host_key"];
        const role = error instanceof ApiClientError ? error.params?.role : undefined;
        const roleText = typeof role === "string" && ["target", "jump", "proxy"].includes(role) ? t(`server.connection.roles.${role}`) : undefined;
        const stageText = typeof stage === "string" && stages.includes(stage) ? t(stageKey) : undefined;
        setResult({ success: false, message: `${translateApiError(error, t)}${roleText ? ` · ${roleText}` : ""}${stageText ? ` · ${stageText}` : ""}` });
      },
    });
  };
  return <div className="space-y-2">
    <Button type="button" variant="outline" onClick={run} disabled={disabled || unsaved || mutation.isPending}>
      {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
      {t(mutation.isPending ? "server.connection.testing" : "server.connection.test")}
    </Button>
    <p className="text-xs text-muted-foreground">{t(unsaved ? "server.connection.saveFirst" : "server.connection.testHint")}</p>
    {!unsaved && result && <p role="status" className={`text-sm ${result.success ? "text-success" : "text-danger"}`}>{result.message}</p>}
  </div>;
}
