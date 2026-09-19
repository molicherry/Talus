import { Loader2 } from "lucide-react";
import { useTranslation } from "../../../i18n";
import { useParams } from "react-router-dom";
import { useService } from "../hooks/use-services";
import { ServiceForm } from "./service-form";

export function ServiceEditPage() {
  const { id } = useParams<{ id: string }>();
  const serviceId = id ? Number(id) : 0;
  const { data: service, isLoading, isError, error } = useService(serviceId);
  const { t } = useTranslation();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (isError || !service) {
    return (
      <div className="rounded-2xl border border-danger/30 bg-danger-subtle p-6 text-center">
        <p className="text-sm text-danger">
          {error instanceof Error ? error.message : t("service.notFound")}
        </p>
      </div>
    );
  }

  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("service.edit")}</h1>
      <ServiceForm service={service} />
    </div>
  );
}
