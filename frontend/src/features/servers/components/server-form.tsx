import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, Plus } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { Link, useLocation, useNavigate } from "react-router-dom";

import { Button } from "../../../components/ui/button";
import { Field, Input, Label, Select, Textarea } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { toast } from "../../../lib/toast";
import { type Server, ServerFormSchema, type ServerFormValues } from "../../../types/models";
import { useCredentials } from "../../credentials/hooks/use-credentials";
import { useCreateServer, useUpdateServer } from "../hooks/use-servers";

interface ServerFormProps {
  server?: Server;
}

export function ServerForm({ server }: ServerFormProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const createMutation = useCreateServer();
  const updateMutation = useUpdateServer();

  const isEdit = !!server;
  const activeMutation = isEdit ? updateMutation : createMutation;
  const { data: credentials } = useCredentials();
  const { t } = useTranslation();

  const {
    register,
    handleSubmit,
    setValue,
    formState: { errors },
  } = useForm<ServerFormValues>({
    resolver: zodResolver(ServerFormSchema),
    defaultValues: server
      ? {
          name: server.name,
          host: server.host,
          port: server.port,
          description: server.description ?? "",
          notes: server.notes ?? "",
          credential_id: server.credential_id ?? undefined,
        }
      : {
          port: 22,
        },
  });

  // The credential create page navigates back here with the id it created, so
  // the user does not have to hunt for it in the dropdown.
  const createdCredentialId = (location.state as { newCredentialId?: number } | null)
    ?.newCredentialId;

  useEffect(() => {
    if (!createdCredentialId) return;
    setValue("credential_id", createdCredentialId);
    // Drop the nav state so a manual refresh does not re-apply the selection.
    navigate(location.pathname, { replace: true, state: null });
  }, [createdCredentialId, setValue, navigate, location.pathname]);

  const onSubmit = (data: ServerFormValues) => {
    if (isEdit && server) {
      updateMutation.mutate(
        { id: server.id, data },
        {
          onSuccess: () => {
            toast.success(t("server.toast.updated"));
            navigate("/servers");
          },
          onError: () => {
            toast.error(t("server.toast.updateFailed"));
          },
        },
      );
    } else {
      createMutation.mutate(data, {
        onSuccess: () => {
          toast.success(t("server.toast.created"));
          navigate("/servers");
        },
        onError: () => {
          toast.error(t("server.toast.createFailed"));
        },
      });
    }
  };

  const errorMessage =
    activeMutation.error instanceof ApiClientError
      ? activeMutation.error.message
      : activeMutation.error
        ? t("common.unexpectedError")
        : null;

  const isPending = activeMutation.isPending;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-4">
      {errorMessage && (
        <div className="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger">
          {errorMessage}
        </div>
      )}

      <Field label={t("server.name")} htmlFor="name" error={errors.name?.message}>
        <Input
          id="name"
          type="text"
          {...register("name")}
          placeholder={t("server.namePlaceholder")}
        />
      </Field>

      <Field label={t("server.host")} htmlFor="host" error={errors.host?.message}>
        <Input
          id="host"
          type="text"
          {...register("host")}
          placeholder={t("server.hostPlaceholder")}
        />
      </Field>

      <Field label={t("server.port")} htmlFor="port" error={errors.port?.message}>
        <Input
          id="port"
          type="number"
          {...register("port", { valueAsNumber: true })}
          placeholder={t("server.portPlaceholder")}
        />
      </Field>

      <Field label={t("server.description")} htmlFor="description" error={errors.description?.message}>
        <Textarea
          id="description"
          {...register("description")}
          rows={3}
          placeholder={t("server.descriptionPlaceholder")}
        />
      </Field>

      <Field label={t("server.notes")} htmlFor="notes" error={errors.notes?.message}>
        <Textarea
          id="notes"
          {...register("notes")}
          rows={4}
          placeholder={t("server.notesPlaceholder")}
        />
      </Field>

      <div>
        <div className="mb-1 flex items-center justify-between gap-2">
          <Label htmlFor="credential_id" className="mb-0">
            {t("server.credential")}
          </Label>
          <Link
            to={`/credentials/new?returnTo=${encodeURIComponent(location.pathname)}`}
            className="inline-flex items-center gap-1 text-xs font-medium text-primary transition-colors hover:text-primary-hover"
            title={t("service.newCredentialHint")}
          >
            <Plus className="h-3.5 w-3.5" />
            {t("service.newCredential")}
          </Link>
        </div>
        <Select
          id="credential_id"
          {...register("credential_id", {
            setValueAs: (v: string) => (v === "" ? undefined : Number(v)),
          })}
        >
          <option value="">{t("server.noCredential")}</option>
          {(credentials ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name || `#${c.id}`} ({c.username}@{c.auth_type})
            </option>
          ))}
        </Select>
      </div>

      <div className="flex gap-3">
        <Button type="submit" disabled={isPending}>
          {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {isPending
            ? isEdit
              ? t("common.updating")
              : t("common.creating")
            : isEdit
              ? t("server.update")
              : t("server.create")}
        </Button>
        <Button type="button" variant="secondary" onClick={() => navigate("/servers")}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
