import { Plus, Server } from "lucide-react";
import { useTranslation } from "../../../i18n";
import { Link } from "react-router-dom";

export function DashboardEmpty() {
  const { t } = useTranslation();

  return (
    <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
      <div className="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl bg-secondary">
        <Server className="h-7 w-7 text-muted-foreground" />
      </div>
      <h3 className="mt-5 text-lg font-semibold text-foreground">{t("dashboard.emptyTitle")}</h3>
      <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
        {t("dashboard.emptyDescription")}
      </p>
      <Link
        to="/servers/new"
        className="mt-6 inline-flex h-9 items-center justify-center gap-2 rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        <Plus className="h-4 w-4" />
        {t("dashboard.addServer")}
      </Link>
    </div>
  );
}
