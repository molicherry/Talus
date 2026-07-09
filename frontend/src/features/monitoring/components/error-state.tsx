import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";

interface ErrorStateProps {
  message: string;
  onRetry: () => void;
}

export function ErrorState({ message, onRetry }: ErrorStateProps) {
  const { t } = useTranslation();
  return (
    <div className="rounded-lg border border-red-300 bg-red-50 p-8 text-center dark:border-red-800 dark:bg-red-900/20">
      <AlertTriangle className="mx-auto h-8 w-8 text-red-600 dark:text-red-400" />
      <p className="mt-3 text-sm text-red-600 dark:text-red-400">{message}</p>
      <button
        type="button"
        onClick={onRetry}
        className="mt-4 rounded-lg bg-red-100 px-4 py-2 text-sm font-medium text-red-600 transition-colors hover:bg-red-200 dark:bg-red-600/20 dark:text-red-400 dark:hover:bg-red-600/30"
      >
        {t("common.retry")}
      </button>
    </div>
  );
}
