import { useTranslation } from "react-i18next";
import { ServiceForm } from "./service-form";
import { ServiceList } from "./service-list";

export function ServiceListPage() {
  const { t } = useTranslation();

  return (
    <div className="space-y-8">
      <h1 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{t("service.title")}</h1>
      <ServiceForm />
      <hr className="border-gray-200 dark:border-gray-800" />
      <ServiceList />
    </div>
  );
}
