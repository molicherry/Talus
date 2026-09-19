import { CircleCheck, CircleOff, Server as ServerIcon } from "lucide-react";

import { Card } from "../../../components/ui/card";
import { useTranslation } from "../../../i18n";
import { cn } from "../../../lib/utils";
import type { Server } from "../../../types/models";

interface DashboardSummaryProps {
  servers: Server[];
}

interface SummaryTileProps {
  label: string;
  value: number;
  icon: typeof ServerIcon;
  tone?: "default" | "success" | "danger";
}

function SummaryTile({ label, value, icon: Icon, tone = "default" }: SummaryTileProps) {
  const toneClass =
    tone === "success" ? "text-success" : tone === "danger" ? "text-danger" : "text-foreground";
  return (
    <Card className="flex items-center gap-3 p-4">
      <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted">
        <Icon className={cn("h-4 w-4", toneClass)} />
      </span>
      <div className="min-w-0">
        <p className={cn("text-xl font-semibold tabular-nums", toneClass)}>{value}</p>
        <p className="truncate text-xs text-muted-foreground">{label}</p>
      </div>
    </Card>
  );
}

export function DashboardSummary({ servers }: DashboardSummaryProps) {
  const { t } = useTranslation();
  const total = servers.length;
  const online = servers.filter((s) => (s.status ?? "unknown") === "online").length;
  const offline = servers.filter((s) => s.status === "offline").length;

  return (
    <div className="mb-8 grid grid-cols-1 gap-4 sm:grid-cols-3">
      <SummaryTile label={t("dashboard.summaryTotal")} value={total} icon={ServerIcon} />
      <SummaryTile
        label={t("dashboard.summaryOnline")}
        value={online}
        icon={CircleCheck}
        tone="success"
      />
      <SummaryTile
        label={t("dashboard.summaryOffline")}
        value={offline}
        icon={CircleOff}
        tone="danger"
      />
    </div>
  );
}
