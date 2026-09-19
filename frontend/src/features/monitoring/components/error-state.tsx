import { AlertTriangle } from "lucide-react";

import { Button } from "../../../components/ui/button";
import { useTranslation } from "../../../i18n";

interface ErrorStateProps {
  message: string;
  onRetry: () => void;
}

export function ErrorState({ message, onRetry }: ErrorStateProps) {
  const { t } = useTranslation();
  return (
    <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
      <AlertTriangle className="mx-auto h-8 w-8 text-danger" />
      <p className="mt-3 text-sm text-danger">{message}</p>
      <Button type="button" variant="outline" onClick={onRetry} className="mt-4">
        {t("common.retry")}
      </Button>
    </div>
  );
}
