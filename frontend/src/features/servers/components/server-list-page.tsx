import { Plus } from "lucide-react";
import { Link } from "react-router-dom";

import { useTranslation } from "../../../i18n";
import { useServers } from "../hooks/use-servers";
import { ServerList } from "./server-list";

export function ServerListPage() {
  const { t } = useTranslation();
  // Shares the `["servers"]` query with <ServerList/>, so this does not add a request.
  const { data: servers } = useServers();

  const count = servers?.length ?? 0;
  const online = (servers ?? []).filter((server) => server.status === "online").length;
  const subtitle =
    count > 0 ? t("server.listSubtitleCount", { count, online }) : t("server.listSubtitle");

  return (
    <div className="mx-auto max-w-7xl">
      <div className="mb-8 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">
            {t("nav.servers")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">{subtitle}</p>
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
