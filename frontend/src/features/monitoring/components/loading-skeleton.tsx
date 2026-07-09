export function LoadingSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="animate-pulse rounded-xl border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-800 dark:bg-gray-900"
        >
          <div className="mb-4 h-3 w-20 rounded bg-gray-200 dark:bg-gray-800" />
          <div className="mx-auto mb-3 h-[140px] w-[140px] rounded-full bg-gray-100 dark:bg-gray-800/50" />
          <div className="h-[60px] rounded bg-gray-100 dark:bg-gray-800/50" />
          <div className="mt-3 flex justify-between border-t border-gray-100 pt-3 dark:border-gray-800">
            <div className="h-3 w-12 rounded bg-gray-200 dark:bg-gray-800" />
            <div className="h-3 w-12 rounded bg-gray-200 dark:bg-gray-800" />
          </div>
        </div>
      ))}
    </div>
  );
}
