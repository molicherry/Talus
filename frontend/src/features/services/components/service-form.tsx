import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ApiClientError } from "../../../lib/api-client";
import { ServiceFormSchema, type ServiceFormValues } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useCreateService } from "../hooks/use-services";
import { ServiceKeyInput } from "./service-key-input";

export function ServiceForm() {
  const navigate = useNavigate();
  const createMutation = useCreateService();
  const { data: servers } = useServers();
  const { t } = useTranslation();

  const {
    register,
    handleSubmit,
    control,
    formState: { errors },
  } = useForm<ServiceFormValues>({
    resolver: zodResolver(ServiceFormSchema),
    defaultValues: {
      credentials: {},
      credential_hints: {},
    },
  });

  const onSubmit = (data: ServiceFormValues) => {
    createMutation.mutate(data, {
      onSuccess: () => {
        toast.success(t("service.toast.created"));
        navigate("/services");
      },
      onError: () => {
        toast.error(t("service.toast.createFailed"));
      },
    });
  };

  const errorMessage =
    createMutation.error instanceof ApiClientError
      ? createMutation.error.message
      : createMutation.error
        ? t("common.unexpectedError")
        : null;

  const isPending = createMutation.isPending;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-4">
      <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100">
        {t("service.create")}
      </h3>

      {errorMessage && (
        <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
          {errorMessage}
        </div>
      )}

      <div>
        <label
          htmlFor="name"
          className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
        >
          {t("service.name")}
        </label>
        <input
          id="name"
          type="text"
          {...register("name")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder="e.g. grafana"
        />
        {errors.name && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.name.message}</p>
        )}
      </div>

      <div>
        <label
          htmlFor="display_name"
          className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
        >
          {t("service.displayName")}
        </label>
        <input
          id="display_name"
          type="text"
          {...register("display_name")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder="e.g. Grafana Dashboard"
        />
        {errors.display_name && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">
            {errors.display_name.message}
          </p>
        )}
      </div>

      <div>
        <label
          htmlFor="base_url"
          className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
        >
          {t("service.baseUrl")}
        </label>
        <input
          id="base_url"
          type="text"
          {...register("base_url")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder="e.g. http://localhost:3000"
        />
        {errors.base_url && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.base_url.message}</p>
        )}
      </div>

      <div>
        <label
          htmlFor="description"
          className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
        >
          {t("service.description")}
        </label>
        <textarea
          id="description"
          {...register("description")}
          rows={3}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
        />
        {errors.description && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">
            {errors.description.message}
          </p>
        )}
      </div>

      <div>
        <label
          htmlFor="server_id"
          className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
        >
          {t("service.server")}
        </label>
        <select
          id="server_id"
          {...register("server_id", {
            setValueAs: (v: string) => (v === "" ? undefined : Number(v)),
          })}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
        >
          <option value="">{t("service.noServer")}</option>
          {(servers ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name} ({s.host})
            </option>
          ))}
        </select>
      </div>

      <div>
        <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {t("service.credentials")}
        </span>
        <Controller
          name="credentials"
          control={control}
          render={({ field }) => (
            <ServiceKeyInput
              value={field.value as Record<string, string>}
              onChange={field.onChange}
            />
          )}
        />
        {errors.credentials && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">
            {(errors.credentials as { message?: string; root?: { message?: string } }).message ||
              ""}
          </p>
        )}
      </div>

      <div>
        <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {t("service.hint")}
        </span>
        <Controller
          name="credential_hints"
          control={control}
          render={({ field }) => (
            <ServiceKeyInput
              value={(field.value ?? {}) as Record<string, string>}
              onChange={field.onChange}
            />
          )}
        />
      </div>

      <div className="flex gap-3">
        <button
          type="submit"
          disabled={isPending}
          className="flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {isPending ? t("common.creating") : t("service.create")}
        </button>
        <button
          type="button"
          onClick={() => navigate("/services")}
          className="rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
        >
          {t("common.cancel")}
        </button>
      </div>
    </form>
  );
}
