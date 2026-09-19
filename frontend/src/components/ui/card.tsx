import * as React from "react";

import { cn } from "../../lib/utils";

/** Themed surface used by every panel/card across the app. */
export function Card({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("rounded-2xl border border-border bg-card shadow-card", className)}
      {...props}
    />
  );
}
