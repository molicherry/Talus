import { Plus } from "lucide-react";
import { useTranslation } from "../../../i18n";
import { Link } from "react-router-dom";
import { CredentialList } from "./credential-list";

export function CredentialListPage() {
  const { t } = useTranslation();
  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-foreground">{t("nav.credentials")}</h1>
        <Link
          to="/credentials/new"
          className="inline-flex items-center gap-2 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover"
        >
          <Plus className="h-4 w-4" />
          {t("credential.add")}
        </Link>
      </div>
      <CredentialList />
    </div>
  );
}
