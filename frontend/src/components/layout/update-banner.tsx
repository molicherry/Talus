import { RefreshCw } from "lucide-react";

import { useUpdateAvailable } from "../../hooks/use-update-available";
import { useTranslation } from "../../i18n";

/**
 * Slim bar shown when the running bundle no longer matches the deployed one.
 * Intentionally not dismissible: the tab is executing code that no longer
 * exists on the server, so the only useful action is a reload.
 */
export function UpdateBanner() {
  const { t } = useTranslation();
  const updateAvailable = useUpdateAvailable();

  if (!updateAvailable) return null;

  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 border-b border-warning/30 bg-warning-subtle px-4 py-2 text-sm text-warning"
    >
      <span>{t("update.available")}</span>
      <button
        type="button"
        onClick={() => window.location.reload()}
        className="inline-flex items-center gap-1.5 rounded-lg border border-warning/40 px-2.5 py-1 text-xs font-medium transition-colors hover:bg-warning/10"
      >
        <RefreshCw className="h-3.5 w-3.5" />
        {t("update.reload")}
      </button>
    </div>
  );
}
