import { useTranslation } from "react-i18next";
import { ServerForm } from "./server-form";

export function ServerCreatePage() {
  const { t } = useTranslation();
  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("server.add")}</h1>
      <ServerForm />
    </div>
  );
}
