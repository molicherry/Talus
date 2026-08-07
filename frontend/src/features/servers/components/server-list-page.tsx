import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ServerList } from "./server-list";

export function ServerListPage() {
  const { t } = useTranslation();
  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">
            {t("nav.servers")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("server.emptyState")}</p>
        </div>
        <Link
          to="/servers/new"
          className="inline-flex h-9 items-center justify-center gap-2 rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        >
          <Plus className="h-4 w-4" />
          {t("server.add")}
        </Link>
      </div>
      <ServerList />
    </div>
  );
}
