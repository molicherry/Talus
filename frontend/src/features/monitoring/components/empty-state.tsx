import { Activity } from "lucide-react";

interface EmptyStateProps {
  message: string;
}

export function EmptyState({ message }: EmptyStateProps) {
  return (
    <div className="rounded-2xl border border-dashed border-border bg-card py-16 text-center">
      <Activity className="mx-auto h-10 w-10 text-muted-foreground/50" />
      <p className="mt-4 text-sm text-muted-foreground">{message}</p>
    </div>
  );
}
