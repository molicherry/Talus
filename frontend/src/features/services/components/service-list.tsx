import { RefreshError } from "../../../components/ui/refresh-error";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { TBody, Table, TableCard, Td, Th, THead } from "../../../components/ui/table";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import type { ServerSummary } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useDeleteService, useServices } from "../hooks/use-services";

function getServerName(servers: ServerSummary[], serverId?: number | null): string {
  if (serverId == null) return "";
  const server = servers.find((s) => s.id === serverId);
  return server ? server.name : "";
}

export function ServiceList() {
  const navigate = useNavigate();
  const { data: services, isLoading, isError, error, isFetching, refetch } = useServices();
  const { data: servers } = useServers();
  const deleteMutation = useDeleteService();
  const { t } = useTranslation();

  const [deleteId, setDeleteId] = useState<number | null>(null);

  const handleDelete = () => {
    if (deleteId === null || deleteMutation.isPending) return;
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
          <div key={id} className="h-14 animate-pulse rounded-xl bg-muted/60" />
        ))}
      </div>
    );
  }

  if (isError && services === undefined) {
    return (
      <div className="rounded-2xl border border-danger/20 bg-danger-subtle p-8 text-center">
        <p className="text-sm text-danger">{translateApiError(error, t, t("service.loadError"))}</p>
        <Button type="button" variant="outline" onClick={() => refetch()} className="mt-4">
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  const refreshError = (
    <RefreshError error={error} isFetching={isFetching} onRetry={() => refetch()} />
  );

  if (!services || services.length === 0) {
    return (
      <>
        {refreshError}
        <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
          <p className="text-muted-foreground">{t("service.emptyState")}</p>
          <Link
            to="/services/new"
            className="mt-4 inline-flex h-9 items-center justify-center gap-2 rounded-lg bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary-hover"
          >
            <Plus className="h-4 w-4" />
            {t("service.add")}
          </Link>
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
              <Th>{t("service.name")}</Th>
              <Th>{t("service.displayName")}</Th>
              <Th>{t("service.baseUrl")}</Th>
              <Th>{t("service.credentials")}</Th>
              <Th>{t("service.server")}</Th>
              <Th align="right" className="sticky right-0 z-10 bg-muted sm:static">
                {t("common.actions")}
              </Th>
            </tr>
          </THead>
          <TBody>
            {services.map((service) => (
              <tr key={service.id} className="transition-colors hover:bg-muted/40">
                <Td className="font-medium text-foreground">{service.name}</Td>
                <Td className="text-foreground">{service.display_name || "—"}</Td>
                <Td className="font-mono text-xs text-muted-foreground">{service.base_url}</Td>
                <Td>
                  <div className="flex flex-wrap gap-1">
                    {service.credential_hints &&
                      Object.keys(service.credential_hints).map((key) => (
                        <span
                          key={key}
                          className="inline-flex items-center rounded-full bg-secondary px-2 py-0.5 text-xs font-medium text-secondary-foreground"
                        >
                          {key}
                        </span>
                      ))}
                  </div>
                </Td>
                <Td className="text-foreground">
                  {getServerName(servers ?? [], service.server_id) || t("service.noServer")}
                </Td>
                <Td className="sticky right-0 bg-card sm:static">
                  <div className="flex items-center justify-end gap-1">
                    <button
                      type="button"
                      onClick={() => navigate(`/services/${service.id}/edit`)}
                      className="touch-target inline-flex items-center justify-center rounded-lg p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                      aria-label={t("service.ariaEdit", { name: service.name })}
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      type="button"
                      onClick={() => setDeleteId(service.id)}
                      className="touch-target inline-flex items-center justify-center rounded-lg p-2 text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
                      aria-label={t("service.ariaDelete", { name: service.name })}
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
