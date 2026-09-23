import { useTranslation } from "../../../i18n";
import { cn } from "../../../lib/utils";
import { TIME_RANGE_MAP, type TimeRange } from "../../../types/metrics";

interface TimeRangeSelectorProps {
  selected: TimeRange;
  onChange: (range: TimeRange) => void;
}

const RANGES: TimeRange[] = ["1h", "6h", "24h", "7d"];

export function TimeRangeSelector({ selected, onChange }: TimeRangeSelectorProps) {
  const { t } = useTranslation();
  return (
    <div role="group" aria-label={t("monitoring.timeRange")} className="inline-flex rounded-lg border border-border bg-muted/60 p-0.5">
      {RANGES.map((range) => (
        <button
          key={range}
          type="button"
          aria-pressed={selected === range}
          onClick={() => onChange(range)}
          className={cn(
            "touch-target rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
            selected === range
              ? "bg-primary text-primary-foreground shadow-sm"
              : "text-muted-foreground hover:bg-secondary hover:text-foreground",
          )}
        >
          {TIME_RANGE_MAP[range].label}
        </button>
      ))}
    </div>
  );
}
