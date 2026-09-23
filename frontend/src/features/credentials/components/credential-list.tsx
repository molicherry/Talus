import { RefreshError } from "../../../components/ui/refresh-error";
import { Pencil, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";

import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { TBody, Table, TableCard, Td, Th, THead } from "../../../components/ui/table";
import { useTranslation } from "../../../i18n";
import { cn } from "../../../lib/utils";
import { toast } from "../../../lib/toast";
import { useCredentials, useDeleteCredential } from "../hooks/use-credentials";

export function CredentialList() {
  const { t } = useTranslation();
  const { data: credentials, isLoading, isError, error, isFetching, refetch } = useCredentials();
  const deleteMutation = useDeleteCredential();

  const [deleteId, setDeleteId] = useState<number | null>(null);

  const handleDelete = () => {
    if (deleteId === null || deleteMutation.isPending) return;
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
          <div key={id} className="h-14 animate-pulse rounded-xl bg-muted/60" />
        ))}
      </div>
    );
  }

  if (isError && credentials === undefined) {
    return (
      <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
        <p className="text-sm text-danger">
          {translateApiError(error, t, t("credential.loadError"))}
        </p>
        <Button type="button" variant="outline" onClick={() => refetch()} className="mt-4">
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  const refreshError = (
    <RefreshError error={error} isFetching={isFetching} onRetry={() => refetch()} />
  );

  if (!credentials || credentials.length === 0) {
    return (
      <>
        {refreshError}
        <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
          <p className="text-muted-foreground">{t("credential.emptyState")}</p>
        </div>
      </>
    );
  }

  return (
    <>
      {refreshError}
      <TableCard>
        <Table>
          <THead>
            <tr>
              <Th>{t("credential.name")}</Th>
              <Th>{t("credential.authType")}</Th>
              <Th>{t("credential.username")}</Th>
              <Th>{t("credential.fingerprint")}</Th>
              <Th align="right" className="sticky right-0 z-10 bg-muted sm:static">
                {t("common.actions")}
              </Th>
            </tr>
          </THead>
          <TBody>
            {credentials.map((credential) => (
              <tr key={credential.id} className="transition-colors hover:bg-muted/40">
                <Td className="font-medium text-foreground">
                  {credential.name || `#${credential.id}`}
                </Td>
                <Td>
                  <span
                    className={cn(
                      "inline-flex rounded-full px-2 py-0.5 text-xs font-medium",
                      credential.auth_type === "password"
                        ? "bg-warning-subtle text-warning"
                        : "bg-success-subtle text-success",
                    )}
                  >
                    {credential.auth_type === "password"
                      ? t("credential.passwordAuth")
                      : t("credential.privateKeyAuth")}
                  </span>
                </Td>
                <Td className="text-foreground">{credential.username}</Td>
                <Td className="text-muted-foreground">{credential.key_fingerprint ?? "—"}</Td>
                <Td className="sticky right-0 bg-card sm:static">
                  <div className="flex items-center justify-end gap-1">
                    <Link
                      to={`/credentials/${credential.id}/edit`}
                      className="touch-target inline-flex items-center justify-center rounded-lg p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                      aria-label={t("credential.ariaEdit")}
                    >
                      <Pencil className="h-4 w-4" />
                    </Link>
                    <button
                      type="button"
                      onClick={() => setDeleteId(credential.id)}
                      className="touch-target inline-flex items-center justify-center rounded-lg p-2 text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
                      aria-label={t("credential.ariaDelete")}
                    >
                      <Trash2 className="h-4 w-4" />
                    </button>
                  </div>
                </Td>
              </tr>
            ))}
          </TBody>
        </Table>
      </TableCard>

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
