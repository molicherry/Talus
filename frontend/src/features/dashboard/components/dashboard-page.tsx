import { useTranslation } from "../../../i18n";
import { useDashboardData } from "../hooks/use-dashboard";
import { DashboardGrid } from "./dashboard-grid";

export function DashboardPage() {
  const { data: servers, isLoading, isError, error, refetch } = useDashboardData();
  const { t } = useTranslation();

  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">
            {t("dashboard.title")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("dashboard.subtitle", "Overview of your server fleet")}
          </p>
        </div>
      </div>
      <DashboardGrid
        servers={servers}
        isLoading={isLoading}
        isError={isError}
        error={error}
        refetch={refetch}
      />
    </div>
  );
}
