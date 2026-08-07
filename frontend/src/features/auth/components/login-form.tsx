import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../../components/ui/button";
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
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <div className="w-full max-w-sm rounded-2xl border border-border bg-card p-8 shadow-elevated">
        <div className="mb-8 text-center">
          <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-sm">
            <Loader2 className="h-6 w-6" />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">{t("app.name")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("auth.signInSubtitle")}</p>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
          {errorMessage && (
            <div className="rounded-lg border border-danger/20 bg-danger-subtle px-4 py-3 text-sm text-danger">
              {errorMessage}
            </div>
          )}

          <div>
            <label htmlFor="username" className="mb-1.5 block text-sm font-medium text-foreground">
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
            <label htmlFor="password" className="mb-1.5 block text-sm font-medium text-foreground">
              {t("auth.password")}
            </label>
            <input
              id="password"
              type="password"
              autoComplete="current-password"
              {...register("password")}
              className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
              placeholder={t("auth.passwordPlaceholder")}
            />
            {errors.password && (
              <p className="mt-1.5 text-xs text-danger">{errors.password.message}</p>
            )}
          </div>

          <Button type="submit" disabled={loginMutation.isPending} className="w-full">
            {loginMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            {loginMutation.isPending ? t("auth.signingIn") : t("auth.signIn")}
          </Button>
        </form>
      </div>
    </div>
  );
}
