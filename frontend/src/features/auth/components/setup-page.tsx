import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";
import { Navigate, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../../components/ui/button";
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
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      {checking ? (
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      ) : !setupNeeded ? (
        <Navigate to="/login" replace />
      ) : (
        <div className="w-full max-w-sm rounded-2xl border border-border bg-card p-8 shadow-elevated">
          <div className="mb-8 text-center">
            <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary-subtle text-primary">
              <ShieldCheck className="h-6 w-6" />
            </div>
            <h1 className="text-2xl font-semibold tracking-tight text-foreground">
              {t("app.name")}
            </h1>
            <p className="mt-1 text-sm text-muted-foreground">Create your admin account</p>
          </div>

          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            {errorMessage && (
              <div className="rounded-lg border border-danger/20 bg-danger-subtle px-4 py-3 text-sm text-danger">
                {errorMessage}
              </div>
            )}

            <div>
              <label
                htmlFor="username"
                className="mb-1.5 block text-sm font-medium text-foreground"
              >
                {t("auth.username")}
              </label>
              <input
                id="username"
                type="text"
                autoComplete="username"
                {...register("username")}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder={t("auth.usernamePlaceholder")}
              />
              {errors.username && (
                <p className="mt-1.5 text-xs text-danger">{errors.username.message}</p>
              )}
            </div>

            <div>
              <label
                htmlFor="password"
                className="mb-1.5 block text-sm font-medium text-foreground"
              >
                {t("auth.password")}
              </label>
              <input
                id="password"
                type="password"
                autoComplete="new-password"
                {...register("password")}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder={t("auth.passwordPlaceholder")}
              />
              {errors.password && (
                <p className="mt-1.5 text-xs text-danger">{errors.password.message}</p>
              )}
            </div>

            <div>
              <label htmlFor="confirm" className="mb-1.5 block text-sm font-medium text-foreground">
                Confirm Password
              </label>
              <input
                id="confirm"
                type="password"
                autoComplete="new-password"
                {...register("confirm")}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder="Re-enter your password"
              />
              {errors.confirm && (
                <p className="mt-1.5 text-xs text-danger">{errors.confirm.message}</p>
              )}
            </div>

            <Button type="submit" disabled={setupMutation.isPending} className="w-full">
              {setupMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              {setupMutation.isPending ? "Creating..." : "Create Admin Account"}
            </Button>
          </form>
        </div>
      )}
    </div>
  );
}
