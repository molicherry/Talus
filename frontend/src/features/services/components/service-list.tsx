import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import type { Server } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useDeleteService, useServices } from "../hooks/use-services";

function getServerName(servers: Server[], serverId?: number | null): string {
  if (serverId == null) return "";
  const server = servers.find((s) => s.id === serverId);
  return server ? server.name : "";
}

export function ServiceList() {
  const navigate = useNavigate();
  const { data: services, isLoading, isError, error, refetch } = useServices();
  const { data: servers } = useServers();
  const deleteMutation = useDeleteService();
  const { t } = useTranslation();

  const [deleteId, setDeleteId] = useState<number | null>(null);

  const handleDelete = () => {
    if (deleteId === null) return;
    deleteMutation.mutate(deleteId, {
      onSuccess: () => {
        toast.success(t("service.toast.deleted"));
        setDeleteId(null);
      },
      onError: () => {
        toast.error(t("service.toast.deleteFailed"));
        setDeleteId(null);
      },
    });
  };

  if (isLoading) {
    return (
      <div className="space-y-3">
        {["sk-1", "sk-2", "sk-3"].map((id) => (
          <div
            key={id}
            className="h-14 animate-pulse rounded-lg bg-gray-100/50 dark:bg-gray-800/50"
          />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-lg border border-red-300 bg-red-50 p-6 text-center dark:border-red-800 dark:bg-red-900/20">
        <p className="text-sm text-red-600 dark:text-red-400">
          {error instanceof Error ? error.message : t("service.loadError")}
        </p>
        <button
          type="button"
          onClick={() => refetch()}
          className="mt-3 rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
        >
          {t("common.retry")}
        </button>
      </div>
    );
  }

  if (!services || services.length === 0) {
    return (
      <div className="rounded-lg border border-gray-200 p-12 text-center dark:border-gray-800">
        <p className="text-gray-500 dark:text-gray-400">{t("service.emptyState")}</p>
        <Link
          to="/services/new"
          className="mt-4 inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700"
        >
          <Plus className="h-4 w-4" />
          {t("service.add")}
        </Link>
      </div>
    );
  }

  return (
    <>
      <div className="overflow-hidden rounded-lg border border-gray-200 dark:border-gray-800">
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-200 bg-gray-50 dark:border-gray-800 dark:bg-gray-900/50">
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("service.name")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("service.displayName")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("service.baseUrl")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("service.credentials")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("service.server")}
              </th>
              <th className="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("common.actions")}
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {services.map((service) => (
              <tr
                key={service.id}
                className="transition-colors hover:bg-gray-100/50 dark:hover:bg-gray-800/50"
              >
                <td className="px-4 py-3">
                  <span className="text-sm font-medium text-gray-900 dark:text-gray-100">
                    {service.name}
                  </span>
                </td>
                <td className="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">
                  {service.display_name || "—"}
                </td>
                <td className="px-4 py-3 font-mono text-xs text-gray-500 dark:text-gray-400">
                  {service.base_url}
                </td>
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
                <td className="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">
                  {getServerName(servers ?? [], service.server_id) || t("service.noServer")}
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-1">
                    <button
                      type="button"
                      onClick={() => navigate(`/services/${service.id}/edit`)}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-gray-700 dark:hover:text-gray-200"
                      aria-label={t("service.ariaEdit", { name: service.name })}
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      type="button"
                      onClick={() => setDeleteId(service.id)}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:text-gray-400 dark:hover:bg-red-900/30 dark:hover:text-red-400"
                      aria-label={t("service.ariaDelete", { name: service.name })}
                    >
                      <Trash2 className="h-4 w-4" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <ConfirmDialog
        open={deleteId !== null}
        title={t("service.deleteTitle")}
        message={t("service.deleteMessage")}
        confirmLabel={t("common.delete")}
        isPending={deleteMutation.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteId(null)}
      />
    </>
  );
}
