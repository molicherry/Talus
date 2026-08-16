interface SparklineProps {
  data: Array<{ value: number }>;
  color: string;
}

const VIEW_W = 100;
const VIEW_H = 32;

function buildSmoothPath(
  data: Array<{ value: number }>,
  w: number,
  h: number,
): { line: string; area: string } {
  if (data.length < 2) return { line: "", area: "" };
  const max = Math.max(...data.map((d) => d.value));
  const scale = max > 0 ? (h - 2) / max : 0;
  const pts = data.map((d, i) => {
    const x = (i / (data.length - 1)) * (w - 2) + 1;
    const y = h - 1 - Math.max(0, Math.min(max, d.value)) * scale;
    return [x, y] as const;
  });
  let line = `M ${pts[0][0]} ${pts[0][1]}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const [x0, y0] = pts[i];
    const [x1, y1] = pts[i + 1];
    const mx = (x0 + x1) / 2;
    line += ` C ${mx} ${y0}, ${mx} ${y1}, ${x1} ${y1}`;
  }
  const area = `${line} L ${pts[pts.length - 1][0]} ${h} L ${pts[0][0]} ${h} Z`;
  return { line, area };
}

export function Sparkline({ data, color }: SparklineProps) {
  if (data.length === 0) return null;
  const { line, area } = buildSmoothPath(data, VIEW_W, VIEW_H);

  return (
    <div className="h-[60px] w-full">
      <svg
        width="100%"
        height="100%"
        viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
        preserveAspectRatio="none"
      >
        <path d={area} fill={color} fillOpacity={0.1} />
        <path
          d={line}
          fill="none"
          stroke={color}
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      </svg>
    </div>
  );
}
