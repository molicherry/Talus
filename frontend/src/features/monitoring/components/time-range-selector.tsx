import { cn } from "../../../lib/utils";
import { TIME_RANGE_MAP, type TimeRange } from "../../../types/metrics";

interface TimeRangeSelectorProps {
  selected: TimeRange;
  onChange: (range: TimeRange) => void;
}

const RANGES: TimeRange[] = ["1h", "6h", "24h", "7d"];

export function TimeRangeSelector({ selected, onChange }: TimeRangeSelectorProps) {
  return (
    <div className="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-gray-700 dark:bg-gray-900">
      {RANGES.map((range) => (
        <button
          key={range}
          type="button"
          onClick={() => onChange(range)}
          className={cn(
            "rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
            selected === range
              ? "bg-indigo-600 text-white shadow-sm"
              : "text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-gray-800 dark:hover:text-gray-200",
          )}
        >
          {TIME_RANGE_MAP[range].label}
        </button>
      ))}
    </div>
  );
}
