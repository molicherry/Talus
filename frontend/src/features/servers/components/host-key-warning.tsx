import { AlertTriangle, Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useAuth } from "../../../hooks/use-auth";
import { ApiClientError } from "../../../lib/api-client";
import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import { invalidateQueries } from "../../../lib/query";
import type { Server } from "../../../types/models";
import { useTestServerConnection, useTrustHostKey } from "../hooks/use-servers";

/**
 * Shown for an untrusted first connection or a changed SSH host key.
 * The handshake is refused until an administrator
 * verifies the new fingerprint out of band and explicitly trusts it — so this
 * surfaces the change instead of leaving the connection failing silently.
 */
export function HostKeyWarning({ server, autoDiscover = false }: { server: Server; autoDiscover?: boolean }) {
  const { t } = useTranslation();
  const { isAdmin } = useAuth();
  const trustMutation = useTrustHostKey();
  const discoverMutation = useTestServerConnection();
  const [discoveryError, setDiscoveryError] = useState<string | null>(null);
  const attempted = useRef<string | null>(null);
  const firstConnection = server.host_key_unverified === true;

  const fingerprint = server.host_key_seen_fingerprint;
  const serverId = server.id;
  const discover = () => {
    setDiscoveryError(null);
    discoverMutation.mutate(serverId, {
      onError: (error) => {
        if (error instanceof ApiClientError && ["ssh_host_key_unverified", "ssh_host_key_mismatch"].includes(error.reason ?? "")) return;
        setDiscoveryError(translateApiError(error, t, t("common.error")));
      },
    });
  };
  const discoverRef = useRef(discover);
  discoverRef.current = discover;
  useEffect(() => {
    const identity = `${serverId}:${server.connection_revision}`;
    if (autoDiscover && isAdmin && firstConnection && !fingerprint && attempted.current !== identity) {
      attempted.current = identity;
      discoverRef.current();
    }
  }, [autoDiscover, isAdmin, firstConnection, fingerprint, serverId, server.connection_revision]);

  if (!firstConnection && !server.host_key_mismatch) return null;

  const handleTrust = () => {
    if (!fingerprint) return;
    // Send the fingerprint we displayed. If the monitor recorded a newer key
    // since then the backend answers 409 and we refresh instead of trusting a
    // key the operator never saw.
    trustMutation.mutate(
      { id: serverId, fingerprint, revision: server.connection_revision },
      {
        onSuccess: () => toast.success(t("server.hostKey.toastTrusted")),
        onError: (error: unknown) => {
          toast.error(translateApiError(error, t, t("common.error")));
          invalidateQueries(["servers"]);
          invalidateQueries(["servers", serverId]);
        },
      },
    );
  };

  return (
    <div role="alert" className={`mb-6 rounded-2xl border border-warning/40 p-4 ${firstConnection ? "bg-card" : "bg-warning-subtle"}`}>
      <div className="flex items-start gap-3">
        <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0 text-warning" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-semibold text-foreground">{t(firstConnection ? "server.hostKey.firstTitle" : "server.hostKey.title")}</h2>
          <p className="mt-1 text-sm text-muted-foreground">{t(firstConnection ? "server.hostKey.firstBody" : "server.hostKey.body")}</p>
          <dl className="mt-3 space-y-1 font-mono text-xs">
            <div className="flex flex-wrap gap-x-2">
              <dt className="text-muted-foreground">{server.name}</dt>
              <dd className="break-all text-foreground">{server.host}:{server.port}</dd>
            </div>
            {server.host_key_fingerprint && (
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">{t("server.hostKey.recorded")}</dt>
                <dd className="break-all text-foreground">{server.host_key_fingerprint}</dd>
              </div>
            )}
            {server.host_key_seen_fingerprint && (
              <div className="flex flex-wrap gap-x-2" data-testid="pending-host-key">
                <dt className="text-muted-foreground">{t("server.hostKey.presented")}</dt>
                <dd className={`break-all ${firstConnection ? "text-foreground" : "text-danger"}`}>{server.host_key_seen_fingerprint}</dd>
              </div>
            )}
          </dl>
          {discoveryError && <p role="status" className="mt-3 text-sm text-danger">{discoveryError}</p>}
          {!isAdmin && <p className="mt-3 text-sm text-muted-foreground">{t("server.hostKey.adminRequired")}</p>}
          {isAdmin && (
            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!fingerprint || trustMutation.isPending || discoverMutation.isPending}
                onClick={handleTrust}
              >
                {trustMutation.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <ShieldCheck className="h-4 w-4" />
                )}
                {t(firstConnection ? "server.hostKey.firstTrust" : "server.hostKey.trust")}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={discoverMutation.isPending || trustMutation.isPending}
                onClick={discover}
              >
                {discoverMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
                {t(discoverMutation.isPending ? "server.hostKey.reading" : fingerprint ? "server.hostKey.readAgain" : "server.hostKey.read")}
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
