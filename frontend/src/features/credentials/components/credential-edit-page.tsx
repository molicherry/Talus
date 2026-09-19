import { zodResolver } from "@hookform/resolvers/zod";
import { Copy, Eye, EyeOff, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useNavigate, useParams } from "react-router-dom";
import { z } from "zod";

import { Button } from "../../../components/ui/button";
import { Field, Input, Label, Textarea } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { cn } from "../../../lib/utils";
import { toast } from "../../../lib/toast";
import { useServers } from "../../servers/hooks/use-servers";
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
  const [showPrivateKey, setShowPrivateKey] = useState(false);

  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t("common.copied"));
    } catch {
      const el = document.createElement("textarea");
      el.value = text;
      el.style.position = "fixed";
      el.style.opacity = "0";
      document.body.appendChild(el);
      el.select();
      document.execCommand("copy");
      document.body.removeChild(el);
      toast.success(t("common.copied"));
    }
  };

  const {
    register,
    handleSubmit,
    watch,
    reset,
    formState: { errors },
  } = useForm<EditFormValues>({
    resolver: zodResolver(EditFormSchema),
    defaultValues: {
      username: credential?.username ?? "",
    },
    values: credential ? { username: credential.username } : undefined,
  });

  const passwordValue = watch("password");

  useEffect(() => {
    if (!credential) return;
    const token = localStorage.getItem("auth_token");
    fetch(`/api/v1/credentials/${credential.id}/reveal`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        if (d?.data) {
          reset({
            username: credential.username,
            password: d.data.password || "",
            private_key: d.data.private_key || "",
          });
        }
      })
      .catch(() => {});
  }, [credential, reset]);

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
        <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
      </div>
    );
  }

  if (!credential) {
    return (
      <div className="rounded-2xl border border-danger/30 bg-danger-subtle p-6 text-center">
        <p className="text-sm text-danger">{t("credential.notFound")}</p>
        <Button type="button" variant="outline" onClick={() => navigate("/credentials")} className="mt-3">
          {t("common.cancel")}
        </Button>
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
      <h1 className="mb-6 text-2xl font-semibold text-foreground">{t("credential.edit")}</h1>

      <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-4">
        {errorMessage && (
          <div className="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger">
            {errorMessage}
          </div>
        )}

        <div>
          <Label>{t("credential.server")}</Label>
          <p className="text-sm text-foreground">
            {linkedServers.length > 0
              ? linkedServers.map((s) => `${s.name} (${s.host})`).join(", ")
              : "—"}
          </p>
        </div>

        <div>
          <Label>{t("credential.authType")}</Label>
          <span
            className={cn(
              "inline-flex rounded-full px-2 py-0.5 text-xs font-medium",
              credential.auth_type === "password"
                ? "bg-warning-subtle text-warning"
                : "bg-success-subtle text-success",
            )}
          >
            {credential.auth_type === "password"
              ? t("credential.passwordAuth")
              : t("credential.privateKeyAuth")}
          </span>
        </div>

        <Field label={t("credential.username")} htmlFor="username" error={errors.username?.message}>
          <Input id="username" type="text" {...register("username")} />
        </Field>

        {credential.auth_type === "password" && (
          <Field label={t("credential.password")} htmlFor="password" error={errors.password?.message}>
            <div className="relative">
              <Input
                id="password"
                type={showPassword ? "text" : "password"}
                {...register("password")}
                className="pr-16"
                placeholder={t("credential.passwordPlaceholderNew")}
              />
              <div className="absolute right-0 top-0 flex h-full items-center gap-0.5 pr-1">
                <button
                  type="button"
                  onClick={() => passwordValue && copyToClipboard(passwordValue)}
                  className="rounded p-1 text-muted-foreground transition-colors hover:text-foreground"
                  aria-label={t("common.copy")}
                >
                  <Copy className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  onClick={() => setShowPassword(!showPassword)}
                  className="rounded p-1 text-muted-foreground transition-colors hover:text-foreground"
                  aria-label={
                    showPassword ? t("credential.hidePassword") : t("credential.showPassword")
                  }
                >
                  {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
            </div>
          </Field>
        )}

        {credential.auth_type === "private_key" && (
          <Field
            label={t("credential.privateKey")}
            htmlFor="private_key"
            error={errors.private_key?.message}
          >
            <div className="relative">
              {showPrivateKey ? (
                <Textarea
                  id="private_key"
                  {...register("private_key")}
                  rows={6}
                  className="pr-16 font-mono text-xs"
                  placeholder={t("credential.privateKeyPlaceholderNew")}
                />
              ) : (
                <Input
                  id="private_key"
                  type="password"
                  {...register("private_key")}
                  className="pr-16"
                  placeholder={t("credential.privateKeyPlaceholderNew")}
                />
              )}
              <div className="absolute right-0 top-1.5 flex gap-0.5 pr-1">
                <button
                  type="button"
                  onClick={() => {
                    const value = watch("private_key");
                    if (value) copyToClipboard(value);
                  }}
                  className="rounded p-1 text-muted-foreground transition-colors hover:text-foreground"
                  aria-label={t("common.copy")}
                >
                  <Copy className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  onClick={() => setShowPrivateKey(!showPrivateKey)}
                  className="rounded p-1 text-muted-foreground transition-colors hover:text-foreground"
                  aria-label={
                    showPrivateKey ? t("credential.hidePassword") : t("credential.showPassword")
                  }
                >
                  {showPrivateKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
            </div>
          </Field>
        )}

        <div className="flex gap-3">
          <Button type="submit" disabled={updateMutation.isPending}>
            {updateMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            {updateMutation.isPending ? t("common.updating") : t("credential.update")}
          </Button>
          <Button type="button" variant="secondary" onClick={() => navigate("/credentials")}>
            {t("common.cancel")}
          </Button>
        </div>
      </form>
    </div>
  );
}
