/**
 * Single source of truth for usage-percentage colour semantics.
 *
 * Previously the dashboard used 65/90 while the monitoring gauges used 60/80,
 * so the same 85% rendered yellow on one page and red on another. Every
 * percentage visual (bars, gauges, cards) must derive its level from here.
 */

export type MetricLevel = "ok" | "warning" | "danger" | "unknown";

/** Usage (%) at or above which a metric is considered elevated. */
export const METRIC_WARNING_PERCENT = 65;
/** Usage (%) at or above which a metric is considered critical. */
export const METRIC_DANGER_PERCENT = 90;

/** Classify a 0–100 usage percentage. `null`/`undefined` means "not collected". */
export function percentLevel(value: number | null | undefined): MetricLevel {
  if (value === null || value === undefined || Number.isNaN(value)) return "unknown";
  if (value < METRIC_WARNING_PERCENT) return "ok";
  if (value < METRIC_DANGER_PERCENT) return "warning";
  return "danger";
}

/** Foreground/text colour class per level. */
export const metricLevelText: Record<MetricLevel, string> = {
  ok: "text-success",
  warning: "text-warning",
  danger: "text-danger",
  unknown: "text-muted-foreground",
};

/** Fill/stroke colour class per level (bars, dots). */
export const metricLevelFill: Record<MetricLevel, string> = {
  ok: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
  unknown: "bg-muted-foreground/40",
};

/** Subtle background class per level (badges, chips). */
export const metricLevelSubtle: Record<MetricLevel, string> = {
  ok: "bg-success-subtle",
  warning: "bg-warning-subtle",
  danger: "bg-danger-subtle",
  unknown: "bg-muted",
};

/** CSS colour variable per level — use in inline `style` for SVG strokes. */
export const metricLevelVar: Record<MetricLevel, string> = {
  ok: "var(--color-success)",
  warning: "var(--color-warning)",
  danger: "var(--color-danger)",
  unknown: "var(--color-muted-foreground)",
};

/** Placeholder rendered wherever a metric was not collected. */
export const METRIC_MISSING_LABEL = "—";

export function formatPercent(value: number | null | undefined, digits = 0): string {
  if (value === null || value === undefined || Number.isNaN(value)) return METRIC_MISSING_LABEL;
  return `${value.toFixed(digits)}%`;
}
