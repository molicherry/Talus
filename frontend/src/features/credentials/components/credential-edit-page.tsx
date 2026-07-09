import { zodResolver } from "@hookform/resolvers/zod";
import { Eye, EyeOff, Loader2 } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import { z } from "zod";
import { useServers } from "../../../features/servers/hooks/use-servers";
import { ApiClientError } from "../../../lib/api-client";
import { useCredentials, useUpdateCredential } from "../hooks/use-credentials";

const EditFormSchema = z.object({
  username: z.string().min(1),
  password: z.string().optional(),
  private_key: z.string().optional(),
});

type EditFormValues = z.infer<typeof EditFormSchema>;

export function CredentialEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { id } = useParams<{ id: string }>();
  const credentialId = Number(id);

  const { data: credentials, isLoading: credentialsLoading } = useCredentials();
  const { data: servers } = useServers();
  const updateMutation = useUpdateCredential();

  const credential = credentials?.find((c) => c.id === credentialId);
  const linkedServers = servers?.filter((s) => s.credential_id === credential?.id) ?? [];

  const [showPassword, setShowPassword] = useState(false);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<EditFormValues>({
    resolver: zodResolver(EditFormSchema),
    defaultValues: {
      username: credential?.username ?? "",
    },
    values: credential ? { username: credential.username } : undefined,
  });

  const onSubmit = (data: EditFormValues) => {
    const payload: { username?: string; password?: string; private_key?: string } = {};

    if (data.username !== credential?.username) {
      payload.username = data.username;
    }
    if (data.password) {
      payload.password = data.password;
    }
    if (data.private_key) {
      payload.private_key = data.private_key;
    }

    updateMutation.mutate(
      { id: credentialId, data: payload },
      {
        onSuccess: () => {
          toast.success(t("credential.toast.updated"));
          navigate("/credentials");
        },
        onError: () => {
          toast.error(t("credential.toast.updateFailed"));
        },
      },
    );
  };

  if (credentialsLoading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <p className="text-sm text-gray-500 dark:text-gray-400">{t("common.loading")}</p>
      </div>
    );
  }

  if (!credential) {
    return (
      <div className="rounded-lg border border-red-300 bg-red-50 p-6 text-center dark:border-red-800 dark:bg-red-900/20">
        <p className="text-sm text-red-600 dark:text-red-400">{t("credential.notFound")}</p>
        <button
          type="button"
          onClick={() => navigate("/credentials")}
          className="mt-3 rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
        >
          {t("common.cancel")}
        </button>
      </div>
    );
  }

  const errorMessage =
    updateMutation.error instanceof ApiClientError
      ? updateMutation.error.message
      : updateMutation.error
        ? t("common.unexpectedError")
        : null;

  return (
    <div>
      <h1 className="mb-6 text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("credential.edit")}</h1>

      <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-4">
        {errorMessage && (
          <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
            {errorMessage}
          </div>
        )}

        <div>
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t("credential.server")}</span>
          <p className="text-sm text-gray-900 dark:text-gray-100">
            {linkedServers.length > 0
              ? linkedServers.map((s) => `${s.name} (${s.host})`).join(", ")
              : "—"}
          </p>
        </div>

        <div>
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t("credential.authType")}</span>
          <span
            className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${
              credential.auth_type === "password"
                ? "bg-blue-50 text-blue-600 dark:bg-blue-900/30 dark:text-blue-400"
                : "bg-purple-50 text-purple-600 dark:bg-purple-900/30 dark:text-purple-400"
            }`}
          >
            {credential.auth_type === "password" ? t("credential.passwordAuth") : t("credential.privateKeyAuth")}
          </span>
        </div>

        <div>
          <label htmlFor="username" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("credential.username")}
          </label>
          <input
            id="username"
            type="text"
            {...register("username")}
            className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          {errors.username && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.username.message}</p>}
        </div>

        {credential.auth_type === "password" && (
          <div>
            <label htmlFor="password" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {t("credential.password")}
            </label>
            <div className="relative">
              <input
                id="password"
                type={showPassword ? "text" : "password"}
                {...register("password")}
                className="w-full rounded-lg border border-gray-700 bg-gray-800 px-3 py-2 pr-10 text-sm text-gray-100 placeholder:text-gray-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                placeholder={t("credential.passwordPlaceholderNew")}
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"
                aria-label={showPassword ? t("credential.hidePassword") : t("credential.showPassword")}
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
            {errors.password && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.password.message}</p>
            )}
          </div>
        )}

        {credential.auth_type === "private_key" && (
          <div>
            <label htmlFor="private_key" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {t("credential.privateKey")}
            </label>
            <textarea
              id="private_key"
              {...register("private_key")}
              rows={6}
              className="w-full rounded-lg border border-gray-700 bg-gray-800 px-3 py-2 font-mono text-xs text-gray-100 placeholder:text-gray-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
              placeholder={t("credential.privateKeyPlaceholderNew")}
            />
            {errors.private_key && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.private_key.message}</p>
            )}
          </div>
        )}

        <div className="flex gap-3">
          <button
            type="submit"
            disabled={updateMutation.isPending}
            className="flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {updateMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            {updateMutation.isPending ? t("common.updating") : t("credential.update")}
          </button>
          <button
            type="button"
            onClick={() => navigate("/credentials")}
            className="rounded-lg bg-gray-200 px-4 py-2 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-300 dark:bg-gray-700 dark:text-gray-200 dark:hover:bg-gray-600"
          >
            {t("common.cancel")}
          </button>
        </div>
      </form>
    </div>
  );
}
