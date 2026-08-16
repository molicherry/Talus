import { Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "../../../i18n";
import { useMutation } from "../../../lib/query";
import { Navigate, useNavigate } from "react-router-dom";
import { Button } from "../../../components/ui/button";
import { ApiClientError, apiClient } from "../../../lib/api-client";
import { setAuthToken } from "../../../lib/auth";
import type { LoginResponse } from "../../../types/api";

interface SetupErrors {
  username?: string;
  password?: string;
  confirm?: string;
}

export function SetupPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [checking, setChecking] = useState(true);
  const [setupNeeded, setSetupNeeded] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<SetupErrors>({});

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

  const setupMutation = useMutation({
    mutationFn: (data: { username: string; password: string }) =>
      apiClient.post<LoginResponse>("/api/v1/auth/login", data),
    onSuccess: (data) => {
      setAuthToken(data.token);
      navigate("/", { replace: true });
    },
  });

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const next: SetupErrors = {};
    if (username.length < 3) next.username = t("validation.usernameRequired");
    if (password.length < 4) next.password = t("validation.passwordRequired");
    if (password !== confirm) next.confirm = "Passwords do not match";
    setErrors(next);
    if (next.username || next.password || next.confirm) return;
    setupMutation.mutate({ username, password });
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

          <form onSubmit={onSubmit} className="space-y-4" noValidate>
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
                value={username}
                onChange={(e) => {
                  setUsername(e.target.value);
                  if (errors.username) setErrors((prev) => ({ ...prev, username: undefined }));
                }}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder={t("auth.usernamePlaceholder")}
              />
              {errors.username && (
                <p className="mt-1.5 text-xs text-danger">{errors.username}</p>
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
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value);
                  if (errors.password) setErrors((prev) => ({ ...prev, password: undefined }));
                }}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder={t("auth.passwordPlaceholder")}
              />
              {errors.password && (
                <p className="mt-1.5 text-xs text-danger">{errors.password}</p>
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
                value={confirm}
                onChange={(e) => {
                  setConfirm(e.target.value);
                  if (errors.confirm) setErrors((prev) => ({ ...prev, confirm: undefined }));
                }}
                className="w-full rounded-lg border border-input bg-card px-3 py-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
                placeholder="Re-enter your password"
              />
              {errors.confirm && (
                <p className="mt-1.5 text-xs text-danger">{errors.confirm}</p>
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
