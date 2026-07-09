import { zodResolver } from "@hookform/resolvers/zod";
import { Eye, EyeOff, Loader2 } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { ApiClientError } from "../../../lib/api-client";
import { CredentialFormSchema, type CredentialFormValues } from "../../../types/models";
import { useCreateCredential } from "../hooks/use-credentials";

export function CredentialForm() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const createMutation = useCreateCredential();

  const [showPassword, setShowPassword] = useState(false);

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<CredentialFormValues>({
    resolver: zodResolver(CredentialFormSchema),
    defaultValues: {
      auth_type: "password",
    },
  });

  const authType = watch("auth_type");

  const onSubmit = (data: CredentialFormValues) => {
    createMutation.mutate(data, {
      onSuccess: () => {
        toast.success(t("credential.toast.created"));
        navigate("/credentials");
      },
      onError: () => {
        toast.error(t("credential.toast.createFailed"));
      },
    });
  };

  const errorMessage =
    createMutation.error instanceof ApiClientError
      ? createMutation.error.message
      : createMutation.error
        ? t("common.unexpectedError")
        : null;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-4">
      {errorMessage && (
        <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
          {errorMessage}
        </div>
      )}

      <div>
        <label htmlFor="name" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {t("credential.name")}
        </label>
        <input
          id="name"
          type="text"
          {...register("name")}
          className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          placeholder={t("credential.namePlaceholder")}
        />
      </div>

      <div>
        <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t("credential.authType")}</span>
        <div className="flex gap-4">
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              type="radio"
              value="password"
              {...register("auth_type")}
              className="border-gray-300 bg-white text-indigo-500 focus:ring-indigo-500 dark:border-gray-600 dark:bg-gray-700"
            />
            {t("credential.passwordAuth")}
          </label>
          <label className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              type="radio"
              value="private_key"
              {...register("auth_type")}
              className="border-gray-300 bg-white text-indigo-500 focus:ring-indigo-500 dark:border-gray-600 dark:bg-gray-700"
            />
            {t("credential.privateKeyAuth")}
          </label>
        </div>
        {errors.auth_type && (
          <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.auth_type.message}</p>
        )}
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
          placeholder={t("credential.usernamePlaceholder")}
        />
        {errors.username && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.username.message}</p>}
      </div>

      {authType === "password" && (
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
              placeholder={t("credential.passwordPlaceholder")}
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

      {authType === "private_key" && (
        <div>
          <label htmlFor="private_key" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {t("credential.privateKey")}
          </label>
          <textarea
            id="private_key"
            {...register("private_key")}
            rows={6}
            className="w-full rounded-lg border border-gray-700 bg-gray-800 px-3 py-2 font-mono text-xs text-gray-100 placeholder:text-gray-500 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            placeholder={t("credential.privateKeyPlaceholder")}
          />
          {errors.private_key && (
            <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.private_key.message}</p>
          )}
        </div>
      )}

      <div className="flex gap-3">
        <button
          type="submit"
          disabled={createMutation.isPending}
          className="flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {createMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {createMutation.isPending ? t("common.creating") : t("credential.create")}
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
  );
}
