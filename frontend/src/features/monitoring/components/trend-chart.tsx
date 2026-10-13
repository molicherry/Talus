import {
  type KeyboardEvent,
  type PointerEvent,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { useTranslation } from "../../../i18n";

import { METRIC_MISSING_LABEL } from "../../../lib/metric-level";
import { isolatedIndices } from "../lib/series";
import { cn } from "../../../lib/utils";

export interface TrendSeries {
  key: string;
  label: string;
  /** CSS colour value, e.g. `var(--color-primary)`. */
  color: string;
  /**
   * Non-colour distinction (REQUIREMENTS §4.5): an SVG dash pattern and a marker
   * shape, so series stay distinguishable without relying on hue.
   */
  dash?: string;
  marker?: "circle" | "square" | "triangle" | "diamond";
  /** One entry per time bucket; `null` marks a bucket with no sample. */
  values: Array<number | null>;
}

/** Small swatch showing a series' dash pattern and marker next to its colour. */
export function SeriesSwatch({ series }: { series: TrendSeries }) {
  const marker = series.marker ?? "circle";
  return (
    <svg width="18" height="10" viewBox="0 0 18 10" aria-hidden="true" className="shrink-0">
      <line
        x1="0"
        y1="5"
        x2="18"
        y2="5"
        stroke={series.color}
        strokeWidth="1.75"
        strokeDasharray={series.dash || undefined}
      />
      {marker === "square" ? (
        <rect x="6" y="2" width="6" height="6" fill={series.color} rx="1" />
      ) : marker === "triangle" ? (
        <polygon points="9,1.5 14,8.5 4,8.5" fill={series.color} />
      ) : marker === "diamond" ? (
        <polygon points="9,1 14,5 9,9 4,5" fill={series.color} />
      ) : (
        <circle cx="9" cy="5" r="2.6" fill={series.color} />
      )}
    </svg>
  );
}

interface TrendChartProps {
  times: string[];
  series: TrendSeries[];
  height?: number;
  /**
   * Fixed y-axis maximum (e.g. 100 for percentages). When omitted the axis is
   * derived from the largest non-null value so throughput series stay readable.
   */
  yMax?: number | null;
  formatValue: (value: number) => string;
  formatAxis: (value: number) => string;
  formatTime: (iso: string) => string;
  className?: string;
  ariaLabel?: string;
  /** Screen-reader description: name, range, granularity, statistic, unit, freshness. */
  description?: string;
}

const PAD = { top: 10, right: 14, bottom: 26, left: 56 } as const;

/** Round a data maximum up to a human-friendly axis bound. */
function niceMax(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const exponent = Math.floor(Math.log10(value));
  const base = 10 ** exponent;
  const norm = value / base;
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10;
  return step * base;
}

export function TrendChart({
  times,
  series,
  height = 200,
  yMax,
  formatValue,
  formatAxis,
  formatTime,
  className,
  ariaLabel,
  description,
}: TrendChartProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [width, setWidth] = useState(640);
  const { t } = useTranslation();
  const helpId = useId();
  // Preserve selection by timestamp when polling shifts or replaces the grid.
  const [selectedTime, setSelectedTime] = useState<string | null>(null);
  // Per-series visibility (REQUIREMENTS §4.5): the legend is a keyboard-operable
  // group of toggle buttons exposing their pressed state.
  const [hiddenKeys, setHiddenKeys] = useState<ReadonlySet<string>>(new Set());
  const toggleSeries = (key: string) =>
    setHiddenKeys((previous) => {
      const next = new Set(previous);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  const visibleSeries = useMemo(
    () => series.filter((s) => !hiddenKeys.has(s.key)),
    [series, hiddenKeys],
  );
  const selectedIndex = selectedTime === null ? -1 : times.indexOf(selectedTime);
  const hoverIndex = selectedIndex < 0 ? null : selectedIndex;

  useEffect(() => {
    const el = containerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const next = entries[0]?.contentRect.width;
      if (next && next > 0) setWidth(next);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const count = times.length;
  const plotW = Math.max(10, width - PAD.left - PAD.right);
  const plotH = Math.max(10, height - PAD.top - PAD.bottom);
  // Bucket timestamps drive every x position (see xAtMs below).
  const timeMs = useMemo(() => times.map((t) => Date.parse(t)), [times]);
  const startMs = count > 0 ? timeMs[0] : 0;
  const endMs = count > 0 ? timeMs[count - 1] : 0;
  const spanMs = endMs - startMs;

  const resolvedMax = useMemo(() => {
    if (yMax !== undefined && yMax !== null) return yMax;
    let max = 0;
    for (const s of visibleSeries) {
      for (const v of s.values) {
        if (v !== null && Number.isFinite(v) && v > max) max = v;
      }
    }
    return niceMax(max);
  }, [visibleSeries, yMax]);

  // Every x position comes from the bucket's real timestamp, so the plot stays
  // proportional to time. Scaling by index would silently compress gaps.
  const xAtMs = (ms: number) =>
    count <= 1 || spanMs <= 0 ? PAD.left + plotW / 2 : PAD.left + ((ms - startMs) / spanMs) * plotW;
  const xAt = (index: number) => xAtMs(timeMs[index]);
  const yAt = (value: number) => PAD.top + plotH - (Math.max(0, value) / resolvedMax) * plotH;

  const gridValues = [1, 0.75, 0.5, 0.25, 0];

  const xTicks = useMemo(() => {
    if (count === 0) return [] as number[];
    if (count === 1) return [0];
    // Even steps across the time span, snapped to the nearest bucket.
    const wanted = 5;
    const ticks: number[] = [];
    for (let i = 0; i < wanted; i++) {
      const targetMs = startMs + (i / (wanted - 1)) * spanMs;
      let best = 0;
      let bestDist = Infinity;
      for (let j = 0; j < count; j++) {
        const dist = Math.abs(timeMs[j] - targetMs);
        if (dist < bestDist) {
          bestDist = dist;
          best = j;
        }
      }
      if (!ticks.includes(best)) ticks.push(best);
    }
    return ticks.sort((a, b) => a - b);
  }, [count, timeMs, startMs, spanMs]);

  /** Contiguous non-null runs, so gaps show as a break rather than a drop to 0. */
  const paths = useMemo(
    () =>
      visibleSeries.map((s) => {
        const segments: string[] = [];
        let current: string[] = [];
        s.values.forEach((value, index) => {
          if (value === null || value === undefined || !Number.isFinite(value)) {
            if (current.length > 0) segments.push(current.join(" "));
            current = [];
            return;
          }
          current.push(
            `${current.length === 0 ? "M" : "L"} ${xAt(index).toFixed(1)} ${yAt(value).toFixed(1)}`,
          );
        });
        if (current.length > 0) segments.push(current.join(" "));
        return { key: s.key, color: s.color, dash: s.dash, d: segments.join(" ") };
        // eslint-disable-next-line react-hooks/exhaustive-deps
      }),
    // xAt/yAt are pure derivations of width/count/series/resolvedMax
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [visibleSeries, width, count, resolvedMax, plotH, timeMs, spanMs],
  );

  // A run of exactly one sample cannot be drawn as a line segment (M without
  // L), so those points are rendered as always-visible dots. Without this an
  // isolated real sample looks empty even though the card shows a value.
  const isolated = useMemo(
    () =>
      visibleSeries.map((s) => ({
        key: s.key,
        color: s.color,
        marker: s.marker ?? "circle",
        points: isolatedIndices(s.values).map((index) => ({
          x: xAt(index),
          y: yAt(s.values[index] as number),
        })),
      })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    // visibleSeries, not series: hiding a series must also remove its isolated
    // markers (R16), and the marker shape keeps them consistent with the legend.
    [visibleSeries, width, count, resolvedMax, plotH, timeMs, spanMs],
  );

  const selectIndex = (index: number) => {
    if (count > 0) setSelectedTime(times[Math.min(count - 1, Math.max(0, index))]);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = hoverIndex ?? count - 1;
    switch (event.key) {
      case "ArrowLeft":
      case "ArrowDown":
        selectIndex(current - 1);
        break;
      case "ArrowRight":
      case "ArrowUp":
        selectIndex(current + 1);
        break;
      case "Home":
        selectIndex(0);
        break;
      case "End":
        selectIndex(count - 1);
        break;
      case "Escape":
        setSelectedTime(null);
        break;
      default:
        return;
    }
    event.preventDefault();
  };

  const handleMove = (event: PointerEvent<SVGRectElement>) => {
    if (count === 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const ratio = (event.clientX - rect.left) / rect.width;
    const targetMs = startMs + Math.min(1, Math.max(0, ratio)) * spanMs;
    let best = 0;
    let bestDist = Infinity;
    for (let i = 0; i < count; i++) {
      const dist = Math.abs(timeMs[i] - targetMs);
      if (dist < bestDist) {
        bestDist = dist;
        best = i;
      }
    }
    selectIndex(best);
  };

  const legend = (
    <div
      role="group"
      aria-label={t("metrics.seriesToggle")}
      className="flex flex-wrap items-center gap-x-2 gap-y-1"
    >
      {series.map((s) => {
        const visible = !hiddenKeys.has(s.key);
        return (
          <button
            key={s.key}
            type="button"
            aria-pressed={visible}
            onClick={() => toggleSeries(s.key)}
            className={cn(
              "touch-target inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-xs transition-colors",
              visible ? "text-muted-foreground" : "text-muted-foreground/60 line-through",
            )}
          >
            <SeriesSwatch series={s} />
            {s.label}
          </button>
        );
      })}
    </div>
  );

  if (count === 0) {
    return (
      <div
        className={cn(
          "flex h-32 items-center justify-center text-sm text-muted-foreground",
          className,
        )}
      >
        {METRIC_MISSING_LABEL}
      </div>
    );
  }

  const hoverX = hoverIndex === null ? 0 : xAt(hoverIndex);
  const tooltipOnLeft = hoverX > PAD.left + plotW * 0.6;
  const tooltipWidth = Math.min(224, Math.max(1, width - 16));
  const currentIndex = hoverIndex ?? count - 1;
  const valueText = [
    formatTime(times[currentIndex]),
    ...visibleSeries.map((s) => {
      const value = s.values[currentIndex];
      return `${s.label}: ${value == null || !Number.isFinite(value) ? t("monitoring.noSample") : formatValue(value)}`;
    }),
  ].join("; ");

  return (
    <div className={cn("w-full", className)}>
      {legend}
      <p id={helpId} className="sr-only">
        {description ? `${description} ` : ""}
        {t("monitoring.chartHelp")}
      </p>
      <div
        ref={containerRef}
        className="relative mt-2 w-full rounded-md"
        role="slider"
        tabIndex={0}
        aria-label={ariaLabel ?? t("monitoring.title")}
        aria-describedby={helpId}
        aria-orientation="horizontal"
        aria-valuemin={0}
        aria-valuemax={count - 1}
        aria-valuenow={currentIndex}
        aria-valuetext={valueText}
        onKeyDown={handleKeyDown}
        onFocus={() => {
          if (hoverIndex === null) selectIndex(count - 1);
        }}
        onBlur={() => setSelectedTime(null)}
      >
        <svg
          width="100%"
          height={height}
          viewBox={`0 0 ${Math.max(1, width)} ${height}`}
          aria-hidden="true"
        >
          {gridValues.map((ratio) => {
            const y = PAD.top + plotH - ratio * plotH;
            return (
              <g key={ratio}>
                <line
                  x1={PAD.left}
                  x2={PAD.left + plotW}
                  y1={y}
                  y2={y}
                  className="stroke-border"
                  strokeWidth={1}
                  strokeDasharray={ratio === 0 ? undefined : "3 4"}
                />
                <text
                  x={PAD.left - 8}
                  y={y + 3}
                  textAnchor="end"
                  className="fill-muted-foreground"
                  fontSize={10}
                >
                  {formatAxis(resolvedMax * ratio)}
                </text>
              </g>
            );
          })}

          {xTicks.map((index) => (
            <text
              key={index}
              x={xAt(index)}
              y={PAD.top + plotH + 16}
              textAnchor={index === 0 ? "start" : index === count - 1 ? "end" : "middle"}
              className="fill-muted-foreground"
              fontSize={10}
            >
              {formatTime(times[index])}
            </text>
          ))}

          {hoverIndex !== null && (
            <line
              x1={hoverX}
              x2={hoverX}
              y1={PAD.top}
              y2={PAD.top + plotH}
              className="stroke-muted-foreground/50"
              strokeWidth={1}
            />
          )}

          {paths.map((p) => (
            <path
              key={p.key}
              d={p.d}
              fill="none"
              stroke={p.color}
              strokeWidth={1.75}
              strokeDasharray={p.dash || undefined}
              strokeLinejoin="round"
              strokeLinecap="round"
            />
          ))}

          {isolated.map((s) =>
            s.points.map((point, index) => {
              const key = `${s.key}-isolated-${index}`;
              if (s.marker === "square") {
                return (
                  <rect key={key} x={point.x - 2.5} y={point.y - 2.5} width={5} height={5} fill={s.color} />
                );
              }
              if (s.marker === "triangle") {
                return (
                  <polygon
                    key={key}
                    points={`${point.x},${point.y - 3} ${point.x + 3},${point.y + 2.5} ${point.x - 3},${point.y + 2.5}`}
                    fill={s.color}
                  />
                );
              }
              if (s.marker === "diamond") {
                return (
                  <polygon
                    key={key}
                    points={`${point.x},${point.y - 3} ${point.x + 3},${point.y} ${point.x},${point.y + 3} ${point.x - 3},${point.y}`}
                    fill={s.color}
                  />
                );
              }
              return <circle key={key} cx={point.x} cy={point.y} r={2.5} fill={s.color} />;
            }),
          )}

          {hoverIndex !== null &&
            visibleSeries.map((s) => {
              const value = s.values[hoverIndex];
              if (value === null || value === undefined || !Number.isFinite(value)) return null;
              return (
                <circle
                  key={s.key}
                  cx={hoverX}
                  cy={yAt(value)}
                  r={3}
                  fill={s.color}
                  className="stroke-card"
                  strokeWidth={1.5}
                />
              );
            })}

          <rect
            x={PAD.left}
            y={PAD.top}
            width={plotW}
            height={plotH}
            fill="transparent"
            style={{ touchAction: "pan-y" }}
            onPointerDown={(event) => {
              event.currentTarget.setPointerCapture(event.pointerId);
              containerRef.current?.focus({ preventScroll: true });
              handleMove(event);
            }}
            onPointerMove={(event) => {
              if (
                event.pointerType === "mouse" ||
                event.currentTarget.hasPointerCapture(event.pointerId)
              )
                handleMove(event);
            }}
            onPointerUp={(event) => {
              if (event.currentTarget.hasPointerCapture(event.pointerId))
                event.currentTarget.releasePointerCapture(event.pointerId);
            }}
            onPointerCancel={() => setSelectedTime(null)}
            onPointerLeave={(event) => {
              if (
                event.pointerType === "mouse" &&
                !event.currentTarget.hasPointerCapture(event.pointerId) &&
                document.activeElement !== containerRef.current
              )
                setSelectedTime(null);
            }}
          />
        </svg>

        {hoverIndex !== null && (
          <div
            className="pointer-events-none absolute top-2 z-10 break-words rounded-lg border border-border bg-card-elevated px-3 py-2 text-xs shadow-dropdown"
            style={{
              width: tooltipWidth,
              left: Math.max(
                8,
                Math.min(
                  width - tooltipWidth - 8,
                  hoverX + (tooltipOnLeft ? -tooltipWidth - 12 : 12),
                ),
              ),
            }}
          >
            <p className="font-medium text-foreground">{formatTime(times[hoverIndex])}</p>
            <div className="mt-1 space-y-0.5">
              {visibleSeries.map((s) => {
                const value = s.values[hoverIndex];
                return (
                  <div key={s.key} className="flex items-center justify-between gap-3">
                    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                      <SeriesSwatch series={s} />
                      {s.label}
                    </span>
                    <span className="font-mono tabular-nums text-foreground">
                      {value === null || value === undefined || !Number.isFinite(value)
                        ? METRIC_MISSING_LABEL
                        : formatValue(value)}
                    </span>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
