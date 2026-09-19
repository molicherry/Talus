import { useTranslation } from "../../../i18n";
import { ServiceForm } from "./service-form";

export function ServiceCreatePage() {
  const { t } = useTranslation();
  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("service.add")}</h1>
      <ServiceForm />
    </div>
  );
}
