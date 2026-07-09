import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { useForm } from "react-hook-form";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ApiClientError } from "../../../lib/api-client";
import { useTranslation } from "react-i18next";
import { type Server, ServerFormSchema, type ServerFormValues } from "../../../types/models";
import { useCredentials } from "../../credentials/hooks/use-credentials";
import { useCreateServer, useUpdateServer } from "../hooks/use-servers";

interface ServerFormProps {
  server?: Server;
}

export function ServerForm({ server }: ServerFormProps) {
  const navigate = useNavigate();
  const createMutation = useCreateServer();
  const updateMutation = useUpdateServer();

  const isEdit = !!server;
  const activeMutation = isEdit ? updateMutation : createMutation;
  const { data: credentials } = useCredentials();
  const { t } = useTranslation();

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<ServerFormValues>({
    resolver: zodResolver(ServerFormSchema),
    defaultValues: server
      ? {
          name: server.name,
          host: server.host,
          port: server.port,
          description: server.description ?? "",
          credential_id: server.credential_id ?? undefined,
        }
      : {
          port: 22,
        },
  });

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
        <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
          {errorMessage}
        </div>
      )}

      <div>
          <label htmlFor="name" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("server.name")}
          </label>
        <input
          id="name"
          type="text"
          {...register("name")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder={t("server.namePlaceholder")}
        />
        {errors.name && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.name.message}</p>}
      </div>

      <div>
          <label htmlFor="host" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("server.host")}
          </label>
        <input
          id="host"
          type="text"
          {...register("host")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder={t("server.hostPlaceholder")}
        />
        {errors.host && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.host.message}</p>}
      </div>

      <div>
          <label htmlFor="port" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("server.port")}
          </label>
        <input
          id="port"
          type="number"
          {...register("port", { valueAsNumber: true })}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder={t("server.portPlaceholder")}
        />
        {errors.port && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.port.message}</p>}
      </div>

      <div>
          <label htmlFor="description" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("server.description")}
          </label>
        <textarea
          id="description"
          {...register("description")}
          rows={3}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder={t("server.descriptionPlaceholder")}
        />
        {errors.description && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.description.message}</p>
        )}
      </div>

      <div>
        <label htmlFor="credential_id" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {t("server.credential")}
        </label>
        <select
          id="credential_id"
          {...register("credential_id", {
            setValueAs: (v: string) => (v === "" ? undefined : Number(v)),
          })}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
        >
          <option value="">{t("server.noCredential")}</option>
          {(credentials ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name || `#${c.id}`} ({c.username}@{c.auth_type})
            </option>
          ))}
        </select>
      </div>

      <div className="flex gap-3">
        <button
          type="submit"
          disabled={isPending}
          className="flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {isPending
            ? isEdit
                ? t("common.updating")
                : t("common.creating")
            : isEdit
                ? t("server.update")
                : t("server.create")}
        </button>
        <button
          type="button"
          onClick={() => navigate("/servers")}
          className="rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
        >
            {t("common.cancel")}
          </button>
      </div>
    </form>
  );
}
