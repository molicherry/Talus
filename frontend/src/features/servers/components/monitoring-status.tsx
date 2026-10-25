import { Link } from "react-router-dom";
import { useAuth } from "../../../hooks/use-auth";
import { useTranslation } from "../../../i18n";
import { formatLocalTime } from "../../../lib/datetime";
import type { MonitoringStatus } from "../../../types/models";

export function AgentMonitoringStatus({ status, compact = false }: { status?: MonitoringStatus; compact?: boolean }) {
  const { t } = useTranslation();
  const { isAdmin } = useAuth();
  if (!status) return null;
  const issue = status.connection !== "online" || status.deployment !== "ready" || status.collection === "partial" || status.collection === "failed" || status.data === "stale";
  const waitingForSample = status.data === "stale" && !status.last_sample_at;
  return (
    <div className={`mt-3 text-xs ${issue ? "text-warning" : "text-muted-foreground"}`}>
      <div className="flex flex-wrap gap-x-3 gap-y-1" aria-live="polite">
        {status.deployment !== "ready" && <span>{t(`agentMonitor.deployment.${status.deployment}`)}</span>}
        <span>{t("agentMonitor.connectionLabel")}: {t(`agentMonitor.connection.${status.connection}`)}</span>
        <span>{t("agentMonitor.collectionLabel")}: {t(`agentMonitor.collection.${status.collection}`)}</span>
        <span>{t("agentMonitor.dataLabel")}: {t(`agentMonitor.data.${waitingForSample ? "waiting" : status.data}`)}</span>
      </div>
      {status.data === "stale" && status.last_sample_at && (
        <p className="mt-1">{t("agentMonitor.staleHint")}</p>
      )}
      {status.deployment_error && <p className="mt-1">{t(`agentMonitor.reasons.${status.deployment_error}`, t("agentMonitor.reasons.deployment_failed"))}</p>}
      {isAdmin && status.deployment_error === "hub_url_required" && <Link className="mt-2 inline-block text-link underline" to="/settings#monitoring">{t("settings.configure")}</Link>}
      {!compact && (
        <div className="mt-3 space-y-1 text-muted-foreground">
          <p>{t("agentMonitor.statusHint")}</p>
          {(status.connection === "unreachable" || status.connection === "reconnecting") && <p>{t("agentMonitor.connectionHint")}</p>}
          {status.data === "backfilling" && <p>{t("agentMonitor.backfillHint")}</p>}
          <p>{t("agentMonitor.contact")}: {formatLocalTime(status.last_contact_at) || "—"}</p>
          <p>{t("agentMonitor.sample")}: {formatLocalTime(status.last_sample_at) || "—"}</p>
          <p>{t("agentMonitor.persisted")}: {formatLocalTime(status.last_persisted_at) || "—"}</p>
          <p>{t("agentMonitor.pending", { count: status.pending_samples })} · {t("agentMonitor.dropped", { count: status.dropped_samples })}</p>
          {status.pending_samples > 0 && <p>{t("agentMonitor.pendingHint")}</p>}
          {status.dropped_samples > 0 && <p>{t("agentMonitor.droppedHint")}</p>}
          {status.version && <p>{t("agentMonitor.version")}: {status.version}</p>}
          {Object.entries(status.errors ?? {}).map(([section, reason]) => (
            <p key={section} className="text-warning">{t(`agentMonitor.sections.${section}`, section)}: {t(`agentMonitor.reasons.${section === "spool" && reason === "write_failed" ? "spool_write_failed" : reason}`, t("agentMonitor.reasons.unknown_error"))}</p>
          ))}
        </div>
      )}
    </div>
  );
}
