import { ArrowUpRight, Cpu, HardDrive, MemoryStick } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { MetricBar } from "../../../components/ui/metric-bar";
import { StatusIndicator } from "../../../components/ui/status-indicator";
import type { Server } from "../../../types/models";

interface ServerCardProps {
  server: Server;
}

export function ServerCard({ server }: ServerCardProps) {
  const navigate = useNavigate();
  const status = server.status ?? "unknown";

  return (
    <button
      type="button"
      onClick={() => navigate(`/servers/${server.id}`)}
      className="group relative flex flex-col rounded-2xl border border-border bg-card p-5 text-left shadow-card transition-all duration-200 hover:border-border-hover hover:shadow-elevated focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
    >
      <div className="mb-4 flex items-start justify-between">
        <div className="flex items-center gap-2.5">
          <StatusIndicator status={status} size="md" />
          <span className="font-semibold text-foreground">{server.name}</span>
        </div>
        <ArrowUpRight className="h-4 w-4 text-muted-foreground opacity-0 transition-all group-hover:opacity-100 group-hover:translate-x-0.5 group-hover:-translate-y-0.5" />
      </div>

      <p className="mb-4 text-xs font-medium text-muted-foreground">{server.host}</p>

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
    </button>
  );
}
