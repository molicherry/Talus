import { Cpu, HardDrive, MemoryStick } from "lucide-react";
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
      className="cursor-pointer rounded-xl border border-gray-200 bg-white p-4 text-left transition-shadow hover:shadow-md dark:border-gray-800 dark:bg-gray-900"
    >
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <StatusIndicator status={status} size="md" />
          <span className="font-semibold text-sm text-gray-900 dark:text-gray-100">
            {server.name}
          </span>
        </div>
        <span className="text-xs text-gray-400 dark:text-gray-500">{server.host}</span>
      </div>

      <div className="space-y-2">
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
