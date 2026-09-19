import { zodResolver } from "@hookform/resolvers/zod";
import { Copy, Eye, EyeOff, Loader2 } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import { Button } from "../../../components/ui/button";
import { Field, Input, Label, Textarea } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { toast } from "../../../lib/toast";
import { CredentialFormSchema, type CredentialFormValues } from "../../../types/models";
import { useCreateCredential } from "../hooks/use-credentials";

export function CredentialForm() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams] = useSearchParams();
  const createMutation = useCreateCredential();

  const [showPassword, setShowPassword] = useState(false);
  // Default to the textarea (multi-line) — browsers strip newlines from
  // <input> values, which would corrupt multi-line OpenSSH private keys.
  const [showPrivateKey, setShowPrivateKey] = useState(true);

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

  // When opened from the server form, return there and let it auto-select the
  // credential we just created.
  const returnTo = searchParams.get("returnTo") ?? location.state?.returnTo ?? null;
  const cancelTarget = returnTo ?? "/credentials";

  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t("common.copied"));
    } catch {
      // Fallback for older browsers or non-secure contexts
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

  const passwordValue = watch("password");

  const onSubmit = (data: CredentialFormValues) => {
    createMutation.mutate(data, {
      onSuccess: (created) => {
        toast.success(t("credential.toast.created"));
        if (returnTo) {
          navigate(returnTo, { state: { newCredentialId: created.id } });
        } else {
          navigate("/credentials");
        }
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
        <div className="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger">
          {errorMessage}
        </div>
      )}

      <Field label={t("credential.name")} htmlFor="name">
        <Input
          id="name"
          type="text"
          {...register("name")}
          placeholder={t("credential.namePlaceholder")}
        />
      </Field>

      <div>
        <Label>{t("credential.authType")}</Label>
        <div className="flex gap-4">
          <label className="flex items-center gap-2 text-sm text-foreground">
            <input
              type="radio"
              value="password"
              {...register("auth_type")}
              className="border-input accent-primary"
            />
            {t("credential.passwordAuth")}
          </label>
          <label className="flex items-center gap-2 text-sm text-foreground">
            <input
              type="radio"
              value="private_key"
              {...register("auth_type")}
              className="border-input accent-primary"
            />
            {t("credential.privateKeyAuth")}
          </label>
        </div>
        {errors.auth_type && <p className="mt-1 text-xs text-danger">{errors.auth_type.message}</p>}
      </div>

      <Field label={t("credential.username")} htmlFor="username" error={errors.username?.message}>
        <Input
          id="username"
          type="text"
          {...register("username")}
          placeholder={t("credential.usernamePlaceholder")}
        />
      </Field>

      {authType === "password" && (
        <Field label={t("credential.password")} htmlFor="password" error={errors.password?.message}>
          <div className="relative">
            <Input
              id="password"
              type={showPassword ? "text" : "password"}
              {...register("password")}
              className="pr-16"
              placeholder={t("credential.passwordPlaceholder")}
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

      {authType === "private_key" && (
        <Field
          label={t("credential.privateKey")}
          htmlFor="private_key"
          // The schema's "credential required" refinement reports on `password`
          // for both auth types, so surface it here when the key field is shown.
          error={errors.password?.message}
        >
          <div className="relative">
            {showPrivateKey ? (
              <Textarea
                id="private_key"
                {...register("private_key")}
                rows={6}
                className="pr-16 font-mono text-xs"
                placeholder={t("credential.privateKeyPlaceholder")}
              />
            ) : (
              <Input
                id="private_key"
                type="password"
                {...register("private_key")}
                className="pr-16"
                placeholder={t("credential.privateKeyPlaceholder")}
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
        <Button type="submit" disabled={createMutation.isPending}>
          {createMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {createMutation.isPending ? t("common.creating") : t("credential.create")}
        </Button>
        <Button type="button" variant="secondary" onClick={() => navigate(cancelTarget)}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
