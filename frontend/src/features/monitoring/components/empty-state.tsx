import { Activity } from "lucide-react";

interface EmptyStateProps {
  message: string;
}

export function EmptyState({ message }: EmptyStateProps) {
  return (
    <div className="rounded-lg border border-gray-200 bg-gray-50 py-16 text-center dark:border-gray-800 dark:bg-gray-900">
      <Activity className="mx-auto h-10 w-10 text-gray-300 dark:text-gray-600" />
      <p className="mt-4 text-sm text-gray-400 dark:text-gray-500">{message}</p>
    </div>
  );
}
