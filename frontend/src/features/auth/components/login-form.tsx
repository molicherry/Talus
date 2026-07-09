import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { z } from "zod";
import { ApiClientError } from "../../../lib/api-client";
import { useLogin } from "../hooks/use-login";

type LoginFormValues = z.infer<ReturnType<typeof createLoginSchema>>;

function createLoginSchema(t: (key: string) => string) {
  return z.object({
    username: z.string().min(1, t("validation.usernameRequired")),
    password: z.string().min(1, t("validation.passwordRequired")),
  });
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const loginSchema = createLoginSchema(t);
  const loginMutation = useLogin();

  useEffect(() => {
    fetch("/api/v1/auth/setup")
      .then((r) => r.json())
      .then((d) => {
        if (d.data?.needed) navigate("/setup", { replace: true });
      })
      .catch(() => {});
  }, [navigate]);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<LoginFormValues>({
    resolver: zodResolver(loginSchema),
  });

  const onSubmit = (data: LoginFormValues) => {
    loginMutation.mutate(data);
  };

  const errorMessage =
    loginMutation.error instanceof ApiClientError
      ? loginMutation.error.message
      : loginMutation.error instanceof Error
        ? loginMutation.error.message
        : loginMutation.error
          ? t("common.unexpectedError")
          : null;

  return (
    <div className="flex min-h-screen items-center justify-center bg-white dark:bg-gray-950">
      <div className="w-full max-w-sm rounded-xl border border-gray-200 bg-gray-50 p-8 shadow-lg dark:border-gray-800 dark:bg-gray-900">
        <div className="mb-8 text-center">
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">{t("app.name")}</h1>
          <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">{t("auth.signInSubtitle")}</p>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
          {errorMessage && (
            <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
              {errorMessage}
            </div>
          )}

          <div>
            <label htmlFor="username" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {t("auth.username")}
            </label>
            <input
              id="username"
              type="text"
              autoComplete="username"
              {...register("username")}
              className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
              placeholder={t("auth.usernamePlaceholder")}
            />
            {errors.username && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.username.message}</p>
            )}
          </div>

          <div>
            <label htmlFor="password" className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {t("auth.password")}
            </label>
            <input
              id="password"
              type="password"
              autoComplete="current-password"
              {...register("password")}
              className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
              placeholder={t("auth.passwordPlaceholder")}
            />
            {errors.password && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">{errors.password.message}</p>
            )}
          </div>

          <button
            type="submit"
            disabled={loginMutation.isPending}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {loginMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            {loginMutation.isPending ? t("auth.signingIn") : t("auth.signIn")}
          </button>
        </form>
      </div>
    </div>
  );
}
