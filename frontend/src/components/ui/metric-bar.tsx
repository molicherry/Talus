import type { LucideIcon } from "lucide-react";

import { cn } from "../../lib/utils";

interface MetricBarProps {
  label: string;
  value: number | null;
  icon?: LucideIcon;
  className?: string;
}

function clampValue(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

function fillColor(value: number | null): string {
  if (value === null) return "bg-gray-300 dark:bg-gray-600";
  if (value < 65) return "bg-green-500";
  if (value < 90) return "bg-yellow-500";
  return "bg-red-500";
}

function formatPercent(value: number | null): string {
  if (value === null) return "N/A";
  return `${Math.round(value)}%`;
}

export function MetricBar({ label, value, icon: Icon, className }: MetricBarProps) {
  const clampedWidth = value !== null ? clampValue(value, 0, 100) : 0;

  return (
    <div className={cn("flex items-center gap-2", className)}>
      {Icon && <Icon className="h-3.5 w-3.5 shrink-0 text-gray-400" />}
      <span className="text-xs text-gray-600 dark:text-gray-400">{label}</span>
      <div className="relative h-1.5 flex-1 overflow-hidden rounded-full bg-gray-200 dark:bg-gray-700">
        {value !== null && (
          <div
            className={cn("h-full rounded-full transition-all", fillColor(value))}
            style={{ width: `${clampedWidth}%` }}
          />
        )}
      </div>
      <span className="w-9 text-right font-mono text-xs tabular-nums text-gray-600 dark:text-gray-400">
        {formatPercent(value)}
      </span>
    </div>
  );
}
