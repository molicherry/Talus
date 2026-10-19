import { useTranslation } from "../../../i18n";
import { formatLocalTime } from "../../../lib/datetime";
import type { MonitoringStatus } from "../../../types/models";

export function AgentMonitoringStatus({ status, compact = false }: { status?: MonitoringStatus; compact?: boolean }) {
  const { t } = useTranslation();
  if (!status) return null;
  const issue = status.deployment !== "ready" || status.collection === "partial" || status.collection === "failed" || status.data === "stale";
  return (
    <div className={`mt-3 text-xs ${issue ? "text-warning" : "text-muted-foreground"}`}>
      <div className="flex flex-wrap gap-x-3 gap-y-1" aria-live="polite">
        {status.deployment !== "ready" && <span>{t(`agentMonitor.deployment.${status.deployment}`)}</span>}
        <span>{t("agentMonitor.collectionLabel")}: {t(`agentMonitor.collection.${status.collection}`)}</span>
        <span>{t("agentMonitor.dataLabel")}: {t(`agentMonitor.data.${status.data}`)}</span>
      </div>
      {status.deployment_error && <p className="mt-1">{t(`agentMonitor.reasons.${status.deployment_error}`, t("agentMonitor.reasons.deployment_failed"))}</p>}
      {!compact && (
        <div className="mt-3 space-y-1 text-muted-foreground">
          <p>{t("agentMonitor.contact")}: {formatLocalTime(status.last_contact_at) || "—"}</p>
          <p>{t("agentMonitor.sample")}: {formatLocalTime(status.last_sample_at) || "—"}</p>
          <p>{t("agentMonitor.persisted")}: {formatLocalTime(status.last_persisted_at) || "—"}</p>
          <p>{t("agentMonitor.pending", { count: status.pending_samples })} · {t("agentMonitor.dropped", { count: status.dropped_samples })}</p>
          {status.version && <p>{t("agentMonitor.version")}: {status.version}</p>}
          {Object.entries(status.errors ?? {}).map(([section, reason]) => (
            <p key={section} className="text-warning">{t(`agentMonitor.sections.${section}`, section)}: {t(`agentMonitor.reasons.${reason}`, t("agentMonitor.reasons.read_failed"))}</p>
          ))}
        </div>
      )}
    </div>
  );
}
