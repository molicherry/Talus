import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { StatusIndicator } from "../../../components/ui/status-indicator";
import { useDeleteServer, useServers } from "../hooks/use-servers";

export function ServerList() {
  const navigate = useNavigate();
  const { data: servers, isLoading, isError, error, refetch } = useServers();
  const deleteMutation = useDeleteServer();
  const { t } = useTranslation();

  const [deleteId, setDeleteId] = useState<number | null>(null);

  const handleDelete = () => {
    if (deleteId === null) return;
    deleteMutation.mutate(deleteId, {
      onSuccess: () => {
        toast.success(t("server.toast.deleted"));
        setDeleteId(null);
      },
      onError: () => {
        toast.error(t("server.toast.deleteFailed"));
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
          {error instanceof Error ? error.message : t("server.loadError")}
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

  if (!servers || servers.length === 0) {
    return (
      <div className="rounded-lg border border-gray-200 p-12 text-center dark:border-gray-800">
        <p className="text-gray-500 dark:text-gray-400">{t("server.emptyState")}</p>
        <Link
          to="/servers/new"
          className="mt-4 inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700"
        >
          <Plus className="h-4 w-4" />
          {t("server.add")}
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
                {t("server.name")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("server.host")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("server.port")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("server.credential")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("server.status.column")}
              </th>
              <th className="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("common.actions")}
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {servers.map((server) => (
              <tr
                key={server.id}
                className="transition-colors hover:bg-gray-100/50 dark:hover:bg-gray-800/50"
              >
                <td className="px-4 py-3">
                  <button
                    type="button"
                    onClick={() => navigate(`/servers/${server.id}`)}
                    className="text-sm font-medium text-indigo-600 transition-colors hover:text-indigo-700 dark:text-indigo-400 dark:hover:text-indigo-300"
                  >
                    {server.name}
                  </button>
                </td>
                <td className="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">
                  {server.host}
                </td>
                <td className="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">
                  {server.port}
                </td>
                <td className="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">
                  {server.credential?.name ?? "—"}
                </td>
                <td className="px-4 py-3">
                  <StatusIndicator status={server.status ?? "unknown"} size="sm" />
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-1">
                    <button
                      type="button"
                      onClick={() => navigate(`/servers/${server.id}/edit`)}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-gray-700 dark:hover:text-gray-200"
                      aria-label={t("server.ariaEdit", { name: server.name })}
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      type="button"
                      onClick={() => setDeleteId(server.id)}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:text-gray-400 dark:hover:bg-red-900/30 dark:hover:text-red-400"
                      aria-label={t("server.ariaDelete", { name: server.name })}
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
        title={t("server.deleteTitle")}
        message={t("server.deleteMessage")}
        confirmLabel={t("common.delete")}
        isPending={deleteMutation.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteId(null)}
      />
    </>
  );
}
