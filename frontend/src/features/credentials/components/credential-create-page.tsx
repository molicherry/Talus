import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import { useTranslation } from "../../../i18n";
import type { SSHCredential } from "../../../types/models";
import { CredentialForm } from "./credential-form";

export function CredentialCreatePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams] = useSearchParams();

  // When opened from the server form, return there and let it auto-select the
  // credential we just created.
  const returnTo = searchParams.get("returnTo") ?? location.state?.returnTo ?? null;
  const cancelTarget = returnTo ?? "/credentials";

  const handleCreated = (created: SSHCredential) => {
    if (returnTo) {
      navigate(returnTo, { state: { newCredentialId: created.id } });
    } else {
      navigate("/credentials");
    }
  };

  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("credential.add")}</h1>
      <CredentialForm onCreated={handleCreated} onCancel={() => navigate(cancelTarget)} />
    </div>
  );
}
