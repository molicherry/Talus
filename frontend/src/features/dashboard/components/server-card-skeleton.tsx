export function ServerCardSkeleton() {
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900">
      <div className="mb-3">
        <div className="h-4 w-24 animate-pulse rounded bg-gray-200 dark:bg-gray-700" />
      </div>
      <div className="space-y-2">
        <div className="h-3 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700" />
        <div className="h-3 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700" />
        <div className="h-3 w-full animate-pulse rounded bg-gray-200 dark:bg-gray-700" />
      </div>
    </div>
  );
}
