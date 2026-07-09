import { Activity, Pencil, Terminal, Trash2, Zap } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import type { Server } from "../../../types/models";
import { useDeleteServer } from "../hooks/use-servers";

interface ServerDetailProps {
  server: Server;
}

export function ServerDetail({ server }: ServerDetailProps) {
  const navigate = useNavigate();
  const deleteMutation = useDeleteServer();
  const [showDelete, setShowDelete] = useState(false);
  const { t } = useTranslation();

  const handleDelete = () => {
    deleteMutation.mutate(server.id, {
      onSuccess: () => {
        toast.success(t("server.toast.deleted"));
        navigate("/servers");
      },
      onError: () => {
        toast.error(t("server.toast.deleteFailed"));
        setShowDelete(false);
      },
    });
  };

  return (
    <>
      <div className="rounded-lg border border-gray-200 bg-gray-50 p-6 dark:border-gray-800 dark:bg-gray-900">
        <div className="flex items-start justify-between">
          <div>
            <h2 className="text-xl font-semibold text-gray-900 dark:text-gray-100">{server.name}</h2>
            <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {server.host}:{server.port}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => navigate(`/servers/${server.id}/edit`)}
              className="rounded-lg bg-gray-200 p-2 text-gray-700 transition-colors hover:bg-gray-300 hover:text-gray-900 dark:bg-gray-700 dark:text-gray-300 dark:hover:bg-gray-600 dark:hover:text-gray-100"
              aria-label={t("server.ariaEdit", { name: server.name })}
            >
              <Pencil className="h-4 w-4" />
            </button>
            <button
              type="button"
              onClick={() => setShowDelete(true)}
              className="rounded-lg bg-red-50 p-2 text-red-600 transition-colors hover:bg-red-100 dark:bg-red-600/20 dark:text-red-400 dark:hover:bg-red-600/30"
              aria-label={t("server.ariaDelete", { name: server.name })}
            >
              <Trash2 className="h-4 w-4" />
            </button>
          </div>
        </div>

        {server.description && <p className="mt-4 text-sm text-gray-500 dark:text-gray-400">{server.description}</p>}

        <div className="mt-6 grid grid-cols-2 gap-4">
          <div className="rounded-lg border border-gray-200 bg-gray-100/50 p-4 dark:border-gray-800 dark:bg-gray-900/50">
            <p className="text-xs font-medium uppercase tracking-wider text-gray-400 dark:text-gray-500">{t("server.host")}</p>
            <p className="mt-1 text-sm text-gray-800 dark:text-gray-200">{server.host}</p>
          </div>
          <div className="rounded-lg border border-gray-200 bg-gray-100/50 p-4 dark:border-gray-800 dark:bg-gray-900/50">
            <p className="text-xs font-medium uppercase tracking-wider text-gray-400 dark:text-gray-500">{t("server.port")}</p>
            <p className="mt-1 text-sm text-gray-800 dark:text-gray-200">{server.port}</p>
          </div>
        </div>
      </div>

      <div className="mt-6 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <button
          type="button"
          onClick={() => navigate(`/servers/${server.id}/exec`)}
          className="flex items-center justify-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-100 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-200 dark:hover:bg-gray-800"
        >
          <Zap className="h-4 w-4 text-indigo-400" />
          {t("server.executeCommand")}
        </button>
        <button
          type="button"
          onClick={() => navigate(`/servers/${server.id}/terminal`)}
          className="flex items-center justify-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-100 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-200 dark:hover:bg-gray-800"
        >
          <Terminal className="h-4 w-4 text-indigo-400" />
          {t("server.terminal")}
        </button>
        <button
          type="button"
          onClick={() => navigate(`/servers/${server.id}/monitoring`)}
          className="flex items-center justify-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-4 py-3 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-100 dark:border-gray-800 dark:bg-gray-900 dark:text-gray-200 dark:hover:bg-gray-800"
        >
          <Activity className="h-4 w-4 text-indigo-400" />
          {t("server.monitoring")}
        </button>
      </div>

      <ConfirmDialog
        open={showDelete}
        title={t("server.deleteTitle")}
        message={t("server.deleteMessageNamed", { name: server.name })}
        confirmLabel={t("common.delete")}
        isPending={deleteMutation.isPending}
        onConfirm={handleDelete}
        onCancel={() => setShowDelete(false)}
      />
    </>
  );
}
