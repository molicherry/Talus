import { useTranslation } from "../../i18n";
import { cn } from "../../lib/utils";

interface StatusIndicatorProps {
  status: "online" | "offline" | "checking" | "unknown";
  showPulse?: boolean;
  showLabel?: boolean;
  size?: "sm" | "md";
  className?: string;
}

const statusColor: Record<StatusIndicatorProps["status"], string> = {
  online: "bg-success",
  offline: "bg-danger",
  checking: "bg-warning",
  unknown: "bg-muted-foreground/60",
};

const pulseColor: Record<string, string> = {
  checking: "bg-warning/75",
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
  showLabel = false,
  size = "md",
  className,
}: StatusIndicatorProps) {
  const { t } = useTranslation();
  // Only an in-flight probe animates; a settled online/offline state is static.
  const shouldPulse = showPulse ?? status === "checking";
  const label = t(`server.status.${status}`);

  const dot = (
    <span className={cn("rounded-full", dotSize[size], statusColor[status])} aria-hidden="true" />
  );

  const pulseOverlay =
    shouldPulse && pulseColor[status] ? (
      <span
        className={cn(
          "absolute inline-flex h-full w-full animate-ping rounded-full",
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
      {showLabel ? (
        <span className="text-xs font-medium text-muted-foreground">{label}</span>
      ) : (
        <span className="sr-only">{label}</span>
      )}
    </span>
  );
}
