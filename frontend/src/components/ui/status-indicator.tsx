import { cn } from "../../lib/utils";

interface StatusIndicatorProps {
  status: "online" | "offline" | "checking" | "unknown";
  showPulse?: boolean;
  size?: "sm" | "md";
  className?: string;
}

const statusColor: Record<StatusIndicatorProps["status"], string> = {
  online: "bg-green-500",
  offline: "bg-red-500",
  checking: "bg-yellow-500",
  unknown: "bg-gray-400",
};

const statusLabel: Record<StatusIndicatorProps["status"], string> = {
  online: "Online",
  offline: "Offline",
  checking: "Checking",
  unknown: "Unknown",
};

const pulseColor: Record<string, string> = {
  online: "bg-green-500/75",
  checking: "bg-yellow-500/75",
};

const dotSize: Record<NonNullable<StatusIndicatorProps["size"]>, string> = {
  sm: "h-2 w-2",
  md: "h-2.5 w-2.5",
};

const wrapperSize: Record<NonNullable<StatusIndicatorProps["size"]>, string> = {
  sm: "h-2 w-2",
  md: "h-2.5 w-2.5",
};

export function StatusIndicator({
  status,
  showPulse,
  size = "md",
  className,
}: StatusIndicatorProps) {
  const shouldPulse = showPulse ?? (status === "online" || status === "checking");

  const dot = (
    <span className={cn("rounded-full", dotSize[size], statusColor[status])} aria-hidden="true" />
  );

  const pulseOverlay =
    shouldPulse && pulseColor[status] ? (
      <span
        className={cn(
          "animate-ping absolute inline-flex h-full w-full rounded-full",
          pulseColor[status],
        )}
      />
    ) : null;

  return (
    <span className={cn("inline-flex items-center gap-1.5", className)} role="status">
      {pulseOverlay ? (
        <span className={cn("relative flex", wrapperSize[size])}>
          {pulseOverlay}
          {dot}
        </span>
      ) : (
        dot
      )}
      <span className="sr-only">{statusLabel[status]}</span>
    </span>
  );
}
