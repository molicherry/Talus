import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";

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

  const data = [
    { name: "Used", value: clamped },
    { name: "Free", value: Math.max(0, 100 - clamped) },
  ];

  return (
    <div className="relative mx-auto" style={{ width: size, height: size }}>
      <ResponsiveContainer width="100%" height="100%">
        <PieChart>
          <Pie
            data={data}
            cx="50%"
            cy="50%"
            innerRadius="70%"
            outerRadius="85%"
            startAngle={90}
            endAngle={-270}
            dataKey="value"
            strokeWidth={0}
            isAnimationActive={true}
            animationDuration={600}
          >
            <Cell fill={color} />
            <Cell fill={TRACK_COLOR} />
          </Pie>
        </PieChart>
      </ResponsiveContainer>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-2xl font-bold tabular-nums text-gray-900 dark:text-gray-100">
          {clamped.toFixed(0)}
        </span>
        <span className="text-[10px] font-medium text-gray-400 dark:text-gray-500">%</span>
      </div>
    </div>
  );
}
