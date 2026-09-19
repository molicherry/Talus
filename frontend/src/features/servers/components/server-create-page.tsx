import { useTranslation } from "../../../i18n";
import { ServerForm } from "./server-form";

export function ServerCreatePage() {
  const { t } = useTranslation();
  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("server.add")}</h1>
      <ServerForm />
    </div>
  );
}
