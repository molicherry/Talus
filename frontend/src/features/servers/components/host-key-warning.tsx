import { AlertTriangle, Loader2, ShieldCheck } from "lucide-react";
import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import type { Server } from "../../../types/models";
import { useTrustHostKey } from "../hooks/use-servers";

/**
 * Shown when a server presented an SSH host key different from the one
 * recorded for it. The handshake is refused (fail-closed) until an operator
 * verifies the new fingerprint out of band and explicitly trusts it — so this
 * surfaces the change instead of leaving the connection failing silently.
 */
export function HostKeyWarning({ server }: { server: Server }) {
  const { t } = useTranslation();
  const trustMutation = useTrustHostKey();

  if (!server.host_key_mismatch) return null;

  const handleTrust = () => {
    trustMutation.mutate(server.id, {
      onSuccess: () => toast.success(t("server.hostKey.toastTrusted")),
      onError: (error: unknown) => toast.error(translateApiError(error, t, t("common.error"))),
    });
  };

  return (
    <div role="alert" className="mb-6 rounded-2xl border border-warning/40 bg-warning-subtle p-4">
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-warning" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold text-foreground">{t("server.hostKey.title")}</h2>
          <p className="mt-1 text-sm text-muted-foreground">{t("server.hostKey.body")}</p>
          <dl className="mt-3 space-y-1 font-mono text-xs">
            {server.host_key_fingerprint && (
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">{t("server.hostKey.recorded")}</dt>
                <dd className="break-all text-foreground">{server.host_key_fingerprint}</dd>
              </div>
            )}
            {server.host_key_seen_fingerprint && (
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">{t("server.hostKey.presented")}</dt>
                <dd className="break-all text-danger">{server.host_key_seen_fingerprint}</dd>
              </div>
            )}
          </dl>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="mt-3"
            disabled={trustMutation.isPending}
            onClick={handleTrust}
          >
            {trustMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <ShieldCheck className="h-4 w-4" />
            )}
            {t("server.hostKey.trust")}
          </Button>
        </div>
      </div>
    </div>
  );
}
