import { Clock, Cpu, Pencil, Terminal, Zap } from "lucide-react";
import { Link } from "react-router-dom";

import { StatusIndicator } from "../../../components/ui/status-indicator";
import { useTranslation } from "../../../i18n";
import type { Server } from "../../../types/models";

interface ServerInfoBarProps {
  server: Server;
}

function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

const actionClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-secondary hover:border-border-hover";

export function ServerInfoBar({ server }: ServerInfoBarProps) {
  const { t } = useTranslation();
  const status = server.status ?? "unknown";
  const metrics = server.latest_metrics;

  return (
    <div className="mb-6 rounded-2xl border border-border bg-card p-5 shadow-card">
      {/* Top row: name + action buttons */}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h1 className="text-xl font-semibold text-foreground">{server.name}</h1>
        <div className="flex flex-wrap items-center gap-2">
          <Link to={`/servers/${server.id}/edit`} className={actionClass}>
            <Pencil className="h-3.5 w-3.5" />
            {t("server.edit")}
          </Link>
          <Link to={`/servers/${server.id}/terminal`} className={actionClass}>
            <Terminal className="h-3.5 w-3.5" />
            {t("server.terminal")}
          </Link>
          <Link to={`/servers/${server.id}/exec`} className={actionClass}>
            <Zap className="h-3.5 w-3.5" />
            {t("server.exec")}
          </Link>
        </div>
      </div>

      {/* Status row */}
      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
        <StatusIndicator status={status} size="sm" showLabel />
        <span className="text-border">|</span>
        <span>
          {server.host}:{server.port}
        </span>
        {server.os && (
          <>
            <span className="text-border">|</span>
            <span>{server.os}</span>
          </>
        )}
        {server.description && (
          <>
            <span className="text-border">|</span>
            <span>{server.description}</span>
          </>
        )}
      </div>

      {/* System info row */}
      {(server.cpu_model || server.uptime_seconds) && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
          {server.cpu_model && (
            <span className="inline-flex items-center gap-1">
              <Cpu className="h-3.5 w-3.5" />
              {server.cpu_model}
            </span>
          )}
          {server.uptime_seconds != null && (
            <span className="inline-flex items-center gap-1">
              <Clock className="h-3.5 w-3.5" />
              {formatUptime(server.uptime_seconds)}
            </span>
          )}
        </div>
      )}

      {/* Metrics row */}
      {metrics && (
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
          {metrics.cpu_percent != null && <span>CPU {metrics.cpu_percent.toFixed(1)}%</span>}
          {metrics.memory_percent != null && <span>Mem {metrics.memory_percent.toFixed(1)}%</span>}
          {metrics.disk_percent != null && <span>Disk {metrics.disk_percent.toFixed(1)}%</span>}
        </div>
      )}
    </div>
  );
}
