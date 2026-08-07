export function ServerCardSkeleton() {
  return (
    <div className="rounded-2xl border border-border bg-card p-5 shadow-card">
      <div className="mb-4 flex items-center gap-2.5">
        <div className="h-2.5 w-2.5 animate-pulse rounded-full bg-muted-foreground/30" />
        <div className="h-4 w-28 animate-pulse rounded bg-muted" />
      </div>
      <div className="mb-4 h-3 w-20 animate-pulse rounded bg-muted-foreground/20" />
      <div className="mt-auto space-y-3">
        <div className="h-3 w-full animate-pulse rounded bg-muted" />
        <div className="h-3 w-full animate-pulse rounded bg-muted" />
        <div className="h-3 w-full animate-pulse rounded bg-muted" />
      </div>
    </div>
  );
}
