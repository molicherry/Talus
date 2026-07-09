import { useTranslation } from "react-i18next";
import { CredentialForm } from "./credential-form";

export function CredentialCreatePage() {
  const { t } = useTranslation();
  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("credential.add")}</h1>
      <CredentialForm />
    </div>
  );
}
