import { Clock, Cpu, Pencil, Terminal, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { StatusIndicator } from "../../../components/ui/status-indicator";
import type { Server } from "../../../types/models";

interface ServerInfoBarProps {
  server: Server;
}

const statusKey: Record<string, string> = {
  online: "server.status.online",
  offline: "server.status.offline",
  checking: "server.status.checking",
  unknown: "server.status.unknown",
};

function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function ServerInfoBar({ server }: ServerInfoBarProps) {
  const { t } = useTranslation();
  const status = server.status ?? "unknown";
  const metrics = server.latest_metrics;

  return (
    <div className="mb-6 rounded-xl border border-gray-200 bg-white p-5 dark:border-gray-800 dark:bg-gray-900">
      {/* Top row: name + action buttons */}
      <div className="flex items-start justify-between">
        <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100">{server.name}</h1>
        <div className="flex items-center gap-2">
          <Link
            to={`/servers/${server.id}/edit`}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50 dark:border-gray-600 dark:text-gray-300 dark:hover:bg-gray-800"
          >
            <Pencil className="h-3.5 w-3.5" />
            {t("server.edit")}
          </Link>
          <Link
            to={`/servers/${server.id}/terminal`}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50 dark:border-gray-600 dark:text-gray-300 dark:hover:bg-gray-800"
          >
            <Terminal className="h-3.5 w-3.5" />
            {t("server.terminal")}
          </Link>
          <Link
            to={`/servers/${server.id}/exec`}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50 dark:border-gray-600 dark:text-gray-300 dark:hover:bg-gray-800"
          >
            <Zap className="h-3.5 w-3.5" />
            {t("server.exec")}
          </Link>
        </div>
      </div>

      {/* Status row */}
      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-gray-600 dark:text-gray-400">
        <span className="inline-flex items-center gap-1.5">
          <StatusIndicator status={status} size="sm" />
          <span>{t(statusKey[status])}</span>
        </span>
        <span className="text-gray-300 dark:text-gray-700">|</span>
        <span>
          {server.host}:{server.port}
        </span>
        {server.os && (
          <>
            <span className="text-gray-300 dark:text-gray-700">|</span>
            <span>{server.os}</span>
          </>
        )}
        {server.description && (
          <>
            <span className="text-gray-300 dark:text-gray-700">|</span>
            <span className="text-gray-500 dark:text-gray-500">
              {server.description}
            </span>
          </>
        )}
      </div>

      {/* System info row */}
      {(server.cpu_model || server.uptime_seconds) && (
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-gray-500 dark:text-gray-400">
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
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-gray-600 dark:text-gray-400">
          {metrics.cpu_percent != null && <span>CPU {metrics.cpu_percent.toFixed(1)}%</span>}
          {metrics.memory_percent != null && <span>Mem {metrics.memory_percent.toFixed(1)}%</span>}
          {metrics.disk_percent != null && <span>Disk {metrics.disk_percent.toFixed(1)}%</span>}
        </div>
      )}
    </div>
  );
}
