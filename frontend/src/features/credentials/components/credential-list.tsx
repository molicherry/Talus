import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { useCredentials, useDeleteCredential } from "../hooks/use-credentials";

export function CredentialList() {
  const { t } = useTranslation();
  const { data: credentials, isLoading, isError, error, refetch } = useCredentials();
  const deleteMutation = useDeleteCredential();

  const [deleteId, setDeleteId] = useState<number | null>(null);

  const handleDelete = () => {
    if (deleteId === null) return;
    deleteMutation.mutate(deleteId, {
      onSuccess: () => {
        toast.success(t("credential.toast.deleted"));
        setDeleteId(null);
      },
      onError: () => {
        toast.error(t("credential.toast.deleteFailed"));
        setDeleteId(null);
      },
    });
  };

  if (isLoading) {
    return (
      <div className="space-y-3">
        {["sk-1", "sk-2", "sk-3"].map((id) => (
          <div key={id} className="h-14 animate-pulse rounded-lg bg-gray-100/50 dark:bg-gray-800/50" />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-lg border border-red-300 bg-red-50 p-6 text-center dark:border-red-800 dark:bg-red-900/20">
        <p className="text-sm text-red-600 dark:text-red-400">
          {error instanceof Error ? error.message : t("credential.loadError")}
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

  if (!credentials || credentials.length === 0) {
    return (
      <div className="rounded-lg border border-gray-200 p-12 text-center dark:border-gray-800">
        <p className="text-gray-500 dark:text-gray-400">{t("credential.emptyState")}</p>
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
                {t("credential.name")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("credential.authType")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("credential.username")}
              </th>
              <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("credential.fingerprint")}
              </th>
              <th className="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                {t("common.actions")}
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {credentials.map((credential) => (
              <tr key={credential.id} className="transition-colors hover:bg-gray-100/50 dark:hover:bg-gray-800/50">
                <td className="px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-300">
                  {credential.name || `#${credential.id}`}
                </td>
                <td className="px-4 py-3 text-sm">
                  <span
                    className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
                      credential.auth_type === "password"
                        ? "bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-400"
                        : "bg-purple-50 text-purple-600 dark:bg-purple-900/30 dark:text-purple-400"
                    }`}
                  >
                    {credential.auth_type === "password" ? t("credential.passwordAuth") : t("credential.privateKeyAuth")}
                  </span>
                </td>
                <td className="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">{credential.username}</td>
                <td className="px-4 py-3 text-sm text-gray-400 dark:text-gray-500">
                  {credential.key_fingerprint ?? "—"}
                </td>
                <td className="px-4 py-3">
                  <div className="flex items-center justify-end gap-1">
                    <Link
                      to={`/credentials/${credential.id}/edit`}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-indigo-50 hover:text-indigo-600 dark:text-gray-400 dark:hover:bg-indigo-900/30 dark:hover:text-indigo-400"
                      aria-label={t("credential.ariaEdit")}
                    >
                      <Pencil className="h-4 w-4" />
                    </Link>
                    <button
                      type="button"
                      onClick={() => setDeleteId(credential.id)}
                      className="rounded-lg p-2 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:text-gray-400 dark:hover:bg-red-900/30 dark:hover:text-red-400"
                      aria-label={t("credential.ariaDelete")}
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
        title={t("credential.deleteTitle")}
        message={t("credential.deleteMessage")}
        confirmLabel={t("common.delete")}
        isPending={deleteMutation.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteId(null)}
      />
    </>
  );
}
