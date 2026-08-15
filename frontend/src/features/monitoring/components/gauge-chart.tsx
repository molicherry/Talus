import { useEffect, useState } from "react";

interface GaugeChartProps {
  value: number;
  size?: number;
}

function getGaugeColor(value: number): string {
  if (value < 60) return "#22c55e";
  if (value < 80) return "#f59e0b";
  return "#ef4444";
}

const TRACK_COLOR = "#d1d5db";

export function GaugeChart({ value, size = 140 }: GaugeChartProps) {
  const clamped = Math.max(0, Math.min(100, value));
  const color = getGaugeColor(clamped);

  // Donut geometry matching recharts Pie (innerRadius 70%, outerRadius 85%)
  const strokeWidth = size * 0.075; // (0.85 - 0.70) * size
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const dash = (clamped / 100) * circumference;

  // Animate the arc from empty on mount, mirroring recharts' 600ms entrance
  const [progress, setProgress] = useState(0);
  useEffect(() => {
    const id = window.setTimeout(() => setProgress(1), 30);
    return () => window.clearTimeout(id);
  }, [clamped]);

  const shown = dash * progress;

  return (
    <div className="relative mx-auto" style={{ width: size, height: size }}>
      <svg width="100%" height="100%" viewBox={`0 0 ${size} ${size}`}>
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke={TRACK_COLOR}
          strokeWidth={strokeWidth}
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeDasharray={`${shown} ${circumference}`}
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
          style={{ transition: "stroke-dasharray 600ms ease" }}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-2xl font-bold tabular-nums text-gray-900 dark:text-gray-100">
          {clamped.toFixed(0)}
        </span>
        <span className="text-[10px] font-medium text-gray-400 dark:text-gray-500">%</span>
      </div>
    </div>
  );
}
