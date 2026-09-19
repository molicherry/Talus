import type { LucideIcon } from "lucide-react";

import { cn } from "../../lib/utils";
import {
  formatPercent,
  METRIC_MISSING_LABEL,
  metricLevelFill,
  percentLevel,
} from "../../lib/metric-level";

interface MetricBarProps {
  label: string;
  value: number | null;
  icon?: LucideIcon;
  className?: string;
}

function clampValue(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

export function MetricBar({ label, value, icon: Icon, className }: MetricBarProps) {
  const hasValue = value !== null && value !== undefined && !Number.isNaN(value);
  const clampedWidth = hasValue ? clampValue(value, 0, 100) : 0;

  return (
    <div className={cn("flex items-center gap-2", className)}>
      {Icon && <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />}
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="relative h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
        {hasValue && (
          <div
            className={cn(
              "h-full rounded-full transition-all duration-500",
              metricLevelFill[percentLevel(value)],
            )}
            style={{ width: `${clampedWidth}%` }}
          />
        )}
      </div>
      <span
        className="w-9 text-right font-mono text-xs tabular-nums text-muted-foreground"
        title={hasValue ? formatPercent(value) : METRIC_MISSING_LABEL}
      >
        {hasValue ? formatPercent(value) : METRIC_MISSING_LABEL}
      </span>
    </div>
  );
}
