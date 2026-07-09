import { useTranslation } from "react-i18next";
import { useDashboardData } from "../hooks/use-dashboard";
import { DashboardGrid } from "./dashboard-grid";

export function DashboardPage() {
  const { data: servers, isLoading, isError, error, refetch } = useDashboardData();
  const { t } = useTranslation();

  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-gray-900 dark:text-gray-100">
        {t("dashboard.title")}
      </h1>
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
