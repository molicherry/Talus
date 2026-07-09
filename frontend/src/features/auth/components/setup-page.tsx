import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";
import { Navigate, useNavigate } from "react-router-dom";
import { z } from "zod";
import { ApiClientError, apiClient } from "../../../lib/api-client";
import { setAuthToken } from "../../../lib/auth";
import type { LoginResponse } from "../../../types/api";

function createSetupSchema(t: (key: string) => string) {
  return z
    .object({
      username: z.string().min(3, t("validation.usernameRequired")),
      password: z.string().min(4, t("validation.passwordRequired")),
      confirm: z.string(),
    })
    .refine((data) => data.password === data.confirm, {
      message: "Passwords do not match",
      path: ["confirm"],
    });
}

type SetupFormValues = z.infer<ReturnType<typeof createSetupSchema>>;

export function SetupPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const setupSchema = createSetupSchema(t);
  const [checking, setChecking] = useState(true);
  const [setupNeeded, setSetupNeeded] = useState(false);

  useEffect(() => {
    fetch("/api/v1/auth/setup")
      .then((res) => res.json())
      .then((data) => {
        if (!data.data?.needed) {
          navigate("/login", { replace: true });
        } else {
          setSetupNeeded(true);
        }
      })
      .catch(() => navigate("/login", { replace: true }))
      .finally(() => setChecking(false));
  }, [navigate]);

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<SetupFormValues>({
    resolver: zodResolver(setupSchema),
  });

  const setupMutation = useMutation({
    mutationFn: (data: { username: string; password: string }) =>
      apiClient.post<LoginResponse>("/api/v1/auth/login", data),
    onSuccess: (data) => {
      setAuthToken(data.token);
      navigate("/", { replace: true });
    },
  });

  const onSubmit = (data: SetupFormValues) => {
    setupMutation.mutate({ username: data.username, password: data.password });
  };

  const errorMessage =
    setupMutation.error instanceof ApiClientError
      ? setupMutation.error.message
      : setupMutation.error instanceof Error
        ? setupMutation.error.message
        : null;

  return (
    <div className="flex min-h-screen items-center justify-center bg-white dark:bg-gray-950">
      {checking ? (
        <Loader2 className="h-6 w-6 animate-spin text-gray-400 dark:text-gray-500" />
      ) : !setupNeeded ? (
        <Navigate to="/login" replace />
      ) : (
      <div className="w-full max-w-sm rounded-xl border border-gray-200 bg-gray-50 p-8 shadow-lg dark:border-gray-800 dark:bg-gray-900">
        <div className="mb-8 text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-50 dark:bg-blue-600/20">
            <ShieldCheck className="h-6 w-6 text-blue-600 dark:text-blue-400" />
          </div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">
            {t("app.name")}
          </h1>
          <p className="mt-1 text-sm text-gray-500 dark:text-gray-400">
            Create your admin account
          </p>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
          {errorMessage && (
            <div className="rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/50 dark:text-red-300">
              {errorMessage}
            </div>
          )}

          <div>
            <label
              htmlFor="username"
              className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
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
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">
                {errors.username.message}
              </p>
            )}
          </div>

          <div>
            <label
              htmlFor="password"
              className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {t("auth.password")}
            </label>
            <input
              id="password"
              type="password"
              autoComplete="new-password"
              {...register("password")}
              className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
              placeholder={t("auth.passwordPlaceholder")}
            />
            {errors.password && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">
                {errors.password.message}
              </p>
            )}
          </div>

          <div>
            <label
              htmlFor="confirm"
              className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              Confirm Password
            </label>
            <input
              id="confirm"
              type="password"
              autoComplete="new-password"
              {...register("confirm")}
              className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
              placeholder="Re-enter your password"
            />
            {errors.confirm && (
              <p className="mt-1 text-xs text-red-600 dark:text-red-400">
                {errors.confirm.message}
              </p>
            )}
          </div>

          <button
            type="submit"
            disabled={setupMutation.isPending}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {setupMutation.isPending && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            {setupMutation.isPending ? "Creating..." : "Create Admin Account"}
          </button>
        </form>
      </div>
      )}
    </div>
  );
}
