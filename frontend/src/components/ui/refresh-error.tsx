import { useTranslation } from "../../i18n";
import { translateApiError } from "../../lib/api-error";
import { Button } from "./button";

/** A failed refresh must not replace data that was already displayed. */
export function RefreshError({
  error,
  isFetching = false,
  onRetry,
}: {
  error: Error | null;
  isFetching?: boolean;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  if (!error) return null;
  return (
    <div
      role="alert"
      className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-warning/30 bg-warning-subtle p-3 text-sm text-foreground"
    >
      <p>
        {t("common.refreshFailed")} {translateApiError(error, t)}
      </p>
      <Button type="button" variant="outline" size="sm" disabled={isFetching} onClick={onRetry}>
        {t(isFetching ? "common.refreshing" : "common.retry")}
      </Button>
    </div>
  );
}
