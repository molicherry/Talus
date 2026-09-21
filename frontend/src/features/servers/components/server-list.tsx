import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { StatusIndicator } from "../../../components/ui/status-indicator";
import { TBody, Table, TableCard, Td, Th, THead } from "../../../components/ui/table";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
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
          <div key={id} className="h-14 animate-pulse rounded-xl bg-muted" />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
        <p className="text-sm text-danger">
          {translateApiError(error, t, t("server.loadError"))}
        </p>
        <Button type="button" variant="outline" onClick={() => refetch()} className="mt-4">
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  if (!servers || servers.length === 0) {
    return (
      <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
        <p className="text-muted-foreground">{t("server.emptyState")}</p>
        <Link
          to="/servers/new"
          className="mt-4 inline-flex h-9 items-center justify-center gap-2 rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover"
        >
          <Plus className="h-4 w-4" />
          {t("server.add")}
        </Link>
      </div>
    );
  }

  return (
    <>
      <TableCard>
        <Table>
          <THead>
            <tr>
              <Th>{t("server.name")}</Th>
              <Th>{t("server.host")}</Th>
              <Th>{t("server.status.column")}</Th>
              <Th align="right">{t("common.actions")}</Th>
            </tr>
          </THead>
          <TBody>
            {servers.map((server) => (
              <tr key={server.id} className="transition-colors hover:bg-muted/40">
                <Td>
                  <button
                    type="button"
                    onClick={() => navigate(`/servers/${server.id}`)}
                    className="text-sm font-medium text-primary transition-colors hover:text-primary-hover hover:underline"
                  >
                    {server.name}
                  </button>
                </Td>
                <Td className="text-muted-foreground">{server.host}</Td>
                <Td>
                  <StatusIndicator status={server.status} size="sm" showLabel />
                </Td>
                <Td>
                  <div className="flex items-center justify-end gap-1">
                    <button
                      type="button"
                      onClick={() => navigate(`/servers/${server.id}/edit`)}
                      className="inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                      aria-label={t("server.ariaEdit", { name: server.name })}
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      type="button"
                      onClick={() => setDeleteId(server.id)}
                      className="inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
                      aria-label={t("server.ariaDelete", { name: server.name })}
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
