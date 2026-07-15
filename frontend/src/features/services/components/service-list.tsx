import { useTranslation } from "react-i18next";
import type { Server } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useServices } from "../hooks/use-services";

function getServerName(servers: Server[], serverId?: number | null): string {
  if (serverId == null) return "";
  const server = servers.find((s) => s.id === serverId);
  return server ? server.name : "";
}

export function ServiceList() {
  const { t } = useTranslation();
  const { data: services, isLoading, isError } = useServices();
  const { data: servers } = useServers();

  if (isLoading) {
    return <p className="py-8 text-center text-sm text-gray-500">{t("common.loading")}</p>;
  }

  if (isError) {
    return <p className="py-8 text-center text-sm text-red-600">{t("service.loadError")}</p>;
  }

  if (!services || services.length === 0) {
    return <p className="py-8 text-center text-sm text-gray-500">{t("service.emptyState")}</p>;
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-gray-200 dark:border-gray-800">
      <table className="w-full text-left text-sm">
        <thead className="bg-gray-50 dark:bg-gray-900">
          <tr>
            <th className="px-4 py-3 font-medium text-gray-700 dark:text-gray-300">
              {t("service.name")}
            </th>
            <th className="px-4 py-3 font-medium text-gray-700 dark:text-gray-300">
              {t("service.displayName")}
            </th>
            <th className="px-4 py-3 font-medium text-gray-700 dark:text-gray-300">
              {t("service.baseUrl")}
            </th>
            <th className="px-4 py-3 font-medium text-gray-700 dark:text-gray-300">
              {t("service.credentials")}
            </th>
            <th className="px-4 py-3 font-medium text-gray-700 dark:text-gray-300">
              {t("service.server")}
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
          {services.map((service) => (
            <tr
              key={service.id}
              className="bg-white hover:bg-gray-50 dark:bg-gray-950 dark:hover:bg-gray-900"
            >
              <td className="px-4 py-3 font-medium text-gray-900 dark:text-gray-100">
                {service.name}
              </td>
              <td className="px-4 py-3 text-gray-600 dark:text-gray-400">
                {service.display_name || "—"}
              </td>
              <td className="px-4 py-3 font-mono text-xs text-gray-500">{service.base_url}</td>
              <td className="px-4 py-3">
                <div className="flex flex-wrap gap-1">
                  {service.credential_hints &&
                    Object.keys(service.credential_hints).map((key) => (
                      <span
                        key={key}
                        className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-gray-800 dark:text-gray-400"
                      >
                        {key}
                      </span>
                    ))}
                </div>
              </td>
              <td className="px-4 py-3 text-gray-600 dark:text-gray-400">
                {getServerName(servers ?? [], service.server_id) || t("service.noServer")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
