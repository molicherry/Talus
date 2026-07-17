import { useTranslation } from "react-i18next";
import { ServiceForm } from "./service-form";

export function ServiceCreatePage() {
  const { t } = useTranslation();
  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("service.add")}</h1>
      <ServiceForm />
    </div>
  );
}
