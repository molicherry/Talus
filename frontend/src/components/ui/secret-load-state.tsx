import { useTranslation } from "../../i18n";
import { translateApiError } from "../../lib/api-error";
import { Button } from "./button";

export function SecretLoadState({
  isLoading,
  error,
  onRetry,
}: {
  isLoading: boolean;
  error?: Error;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  if (isLoading)
    return (
      <p role="status" className="text-sm text-muted-foreground">
        {t("common.loadingSecret")}
      </p>
    );
  if (!error) return null;
  return (
    <div
      role="alert"
      className="rounded-lg border border-danger/30 bg-danger-subtle p-3 text-sm text-danger"
    >
      <p>
        {t("common.secretLoadFailed")} {translateApiError(error, t)}
      </p>
      <Button type="button" variant="outline" onClick={onRetry} className="mt-2">
        {t("common.retry")}
      </Button>
    </div>
  );
}
