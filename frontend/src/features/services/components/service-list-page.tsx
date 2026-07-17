import { Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ServiceList } from "./service-list";

export function ServiceListPage() {
  const { t } = useTranslation();
  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("nav.services")}</h1>
        <Link
          to="/services/new"
          className="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700"
        >
          <Plus className="h-4 w-4" />
          {t("service.add")}
        </Link>
      </div>
      <ServiceList />
    </div>
  );
}
