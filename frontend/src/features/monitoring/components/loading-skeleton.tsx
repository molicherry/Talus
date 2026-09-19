export function LoadingSkeleton() {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="animate-pulse rounded-2xl border border-border bg-card p-4">
            <div className="h-3 w-16 rounded bg-muted" />
            <div className="mt-4 h-7 w-20 rounded bg-muted" />
            <div className="mt-3 h-3 w-24 rounded bg-muted/70" />
          </div>
        ))}
      </div>
      <div className="animate-pulse rounded-2xl border border-border bg-card p-5">
        <div className="h-3 w-24 rounded bg-muted" />
        <div className="mt-4 h-[240px] rounded bg-muted/60" />
      </div>
    </div>
  );
}
