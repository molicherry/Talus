import { Cpu, HardDrive, MemoryStick, Terminal } from "lucide-react";
import { Link } from "react-router-dom";

import { MetricBar } from "../../../components/ui/metric-bar";
import { StatusIndicator } from "../../../components/ui/status-indicator";
import { useTranslation } from "../../../i18n";
import { formatLocalTime } from "../../../lib/datetime";
import type { Server } from "../../../types/models";

interface ServerCardProps {
  server: Server;
}

export function ServerCard({ server }: ServerCardProps) {
  const { t } = useTranslation();
  const status = server.status ?? "unknown";
  const lastSeen = formatLocalTime(server.last_seen);

  return (
    <div className="group relative flex flex-col rounded-2xl border border-border bg-card p-5 shadow-card transition-all duration-200 hover:border-border-hover hover:shadow-elevated focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 focus-within:ring-offset-background">
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-semibold text-foreground">
            {/* Stretched link: the whole card navigates to the detail page… */}
            <Link
              to={`/servers/${server.id}`}
              className="after:absolute after:inset-0 focus-visible:outline-none"
            >
              {server.name}
            </Link>
          </h3>
          <p className="mt-0.5 truncate text-xs font-medium text-muted-foreground">{server.host}</p>
        </div>
        <StatusIndicator status={status} size="sm" showLabel className="shrink-0 pt-1" />
      </div>

      <div className="mt-auto space-y-3">
        <MetricBar label="CPU" value={server.latest_metrics?.cpu_percent ?? null} icon={Cpu} />
        <MetricBar
          label="Mem"
          value={server.latest_metrics?.memory_percent ?? null}
          icon={MemoryStick}
        />
        <MetricBar
          label="Disk"
          value={server.latest_metrics?.disk_percent ?? null}
          icon={HardDrive}
        />
      </div>

      <div className="mt-4 flex items-center justify-between gap-2 border-t border-border pt-3 text-xs">
        <span className="truncate text-muted-foreground">
          {lastSeen ? t("server.lastSeen", { time: lastSeen }) : t("server.neverSeen")}
        </span>
        {/* …while this shortcut stays clickable on top of the stretched link. */}
        <Link
          to={`/servers/${server.id}/terminal`}
          className="relative z-10 inline-flex shrink-0 items-center gap-1 font-medium text-primary transition-colors hover:text-primary-hover"
        >
          <Terminal className="h-3.5 w-3.5" />
          {t("server.terminal")}
        </Link>
      </div>
    </div>
  );
}
