import { Plus, Server } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

export function DashboardEmpty() {
  const { t } = useTranslation();

  return (
    <div className="rounded-lg border border-gray-200 p-12 text-center dark:border-gray-800">
      <Server className="mx-auto h-12 w-12 text-gray-300 dark:text-gray-600" />
      <h3 className="mt-4 text-lg font-semibold text-gray-900 dark:text-gray-100">
        {t("dashboard.emptyTitle")}
      </h3>
      <p className="mt-2 text-sm text-gray-500 dark:text-gray-400">
        {t("dashboard.emptyDescription")}
      </p>
      <Link
        to="/servers/new"
        className="mt-6 inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700"
      >
        <Plus className="h-4 w-4" />
        {t("dashboard.addServer")}
      </Link>
    </div>
  );
}
