import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useNavigate } from "react-router-dom";

import { Button } from "../../../components/ui/button";
import { Field, Input, Label, Select, Textarea } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { toast } from "../../../lib/toast";
import { type Service, ServiceFormSchema, type ServiceFormValues } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useCreateService, useUpdateService } from "../hooks/use-services";
import { ServiceKeyInput } from "./service-key-input";

interface ServiceFormProps {
  service?: Service;
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="border-b border-border pb-2 text-sm font-semibold text-foreground">{children}</h3>
  );
}

export function ServiceForm({ service }: ServiceFormProps) {
  const navigate = useNavigate();
  const createMutation = useCreateService();
  const updateMutation = useUpdateService();

  const isEdit = !!service;
  const activeMutation = isEdit ? updateMutation : createMutation;
  const { data: servers } = useServers();
  const { t } = useTranslation();

  const [showGuide, setShowGuide] = useState(!!service?.usage_guide);
  const [showGuidePreview, setShowGuidePreview] = useState(false);

  const {
    register,
    handleSubmit,
    control,
    reset,
    setValue,
    formState: { errors },
  } = useForm<ServiceFormValues>({
    resolver: zodResolver(ServiceFormSchema),
    defaultValues: service
      ? {
          name: service.name,
          display_name: service.display_name,
          base_url: service.base_url,
          description: service.description ?? "",
          usage_guide: service.usage_guide ?? "",
          server_id: service.server_id ?? undefined,
          credentials: {},
          credential_hints: service.credential_hints ?? {},
        }
      : {
          credentials: {},
          credential_hints: {},
        },
  });

  const hints = useWatch({ control, name: "credential_hints" }) ?? {};
  const guideValue = useWatch({ control, name: "usage_guide" }) ?? "";

  useEffect(() => {
    if (!service) return;
    const token = localStorage.getItem("auth_token");
    fetch(`/api/v1/services/${service.id}/credentials`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((res) => res.json())
      .then((data) => {
        if (data.data && typeof data.data === "object") {
          const creds = data.data as Record<string, string>;
          reset({
            name: service.name,
            display_name: service.display_name,
            base_url: service.base_url,
            description: service.description ?? "",
            usage_guide: service.usage_guide ?? "",
            server_id: service.server_id ?? undefined,
            credentials: creds,
            credential_hints: service.credential_hints ?? {},
          });
        }
      })
      .catch(() => {});
  }, [service, reset]);

  const onSubmit = (data: ServiceFormValues) => {
    if (isEdit && service) {
      updateMutation.mutate(
        { id: service.id, data },
        {
          onSuccess: () => {
            toast.success(t("service.toast.updated"));
            navigate("/services");
          },
          onError: () => {
            toast.error(t("service.toast.updateFailed"));
          },
        },
      );
    } else {
      createMutation.mutate(data, {
        onSuccess: () => {
          toast.success(t("service.toast.created"));
          navigate("/services");
        },
        onError: () => {
          toast.error(t("service.toast.createFailed"));
        },
      });
    }
  };

  const errorMessage =
    activeMutation.error instanceof ApiClientError
      ? activeMutation.error.message
      : activeMutation.error
        ? t("common.unexpectedError")
        : null;

  const isPending = activeMutation.isPending;

  return (
    <form onSubmit={handleSubmit(onSubmit)} className="max-w-lg space-y-6">
      {errorMessage && (
        <div className="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger">
          {errorMessage}
        </div>
      )}

      <section className="space-y-4">
        <SectionTitle>{t("service.sectionBasics")}</SectionTitle>

        <Field label={t("service.name")} htmlFor="name" error={errors.name?.message}>
          <Input id="name" type="text" {...register("name")} placeholder="e.g. grafana" />
        </Field>

        <Field
          label={t("service.displayName")}
          htmlFor="display_name"
          error={errors.display_name?.message}
        >
          <Input
            id="display_name"
            type="text"
            {...register("display_name")}
            placeholder="e.g. Grafana Dashboard"
          />
        </Field>

        <Field label={t("service.baseUrl")} htmlFor="base_url" error={errors.base_url?.message}>
          <Input
            id="base_url"
            type="text"
            {...register("base_url")}
            placeholder="e.g. http://localhost:3000"
          />
        </Field>

        <Field label={t("service.description")} htmlFor="description" error={errors.description?.message}>
          <Textarea id="description" {...register("description")} rows={3} />
        </Field>
      </section>

      <section className="space-y-4">
        <SectionTitle>{t("service.sectionConnection")}</SectionTitle>

        <Field label={t("service.server")} htmlFor="server_id">
          <Select
            id="server_id"
            {...register("server_id", {
              setValueAs: (v: string) => (v === "" ? undefined : Number(v)),
            })}
          >
            <option value="">{t("service.noServer")}</option>
            {(servers ?? []).map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({s.host})
              </option>
            ))}
          </Select>
        </Field>

        <div>
          <Label>{t("service.credentials")}</Label>
          <Controller
            name="credentials"
            control={control}
            render={({ field }) => (
              <ServiceKeyInput
                value={{
                  credentials: (field.value ?? {}) as Record<string, string>,
                  hints: hints as Record<string, string>,
                }}
                onChange={(v) => {
                  field.onChange(v.credentials);
                  setValue("credential_hints", v.hints as Record<string, string>);
                }}
              />
            )}
          />
          {isEdit && (
            <p className="mt-1 text-xs text-muted-foreground">
              {t("service.credentialsEditNote")}
            </p>
          )}
          {errors.credentials && (
            <p className="mt-1 text-xs text-danger">
              {(errors.credentials as { message?: string; root?: { message?: string } }).message ||
                ""}
            </p>
          )}
        </div>
      </section>

      <section className="space-y-4">
        <button
          type="button"
          onClick={() => setShowGuide((v) => !v)}
          className="flex w-full items-center gap-2 border-b border-border pb-2 text-left text-sm font-semibold text-foreground"
          aria-expanded={showGuide}
        >
          {showGuide ? (
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-4 w-4 text-muted-foreground" />
          )}
          {t("service.sectionUsageGuide")}
        </button>

        {showGuide && (
          <div>
            <div className="mb-1 flex items-center justify-end">
              <button
                type="button"
                onClick={() => setShowGuidePreview((v) => !v)}
                className="text-xs font-medium text-primary transition-colors hover:text-primary-hover"
              >
                {showGuidePreview ? t("service.usageGuideEdit") : t("service.usageGuidePreview")}
              </button>
            </div>
            {showGuidePreview ? (
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-muted/40 p-3 font-mono text-xs text-foreground">
                {guideValue ? guideValue : t("service.usageGuideEmpty")}
              </pre>
            ) : (
              <Textarea
                id="usage_guide"
                {...register("usage_guide")}
                rows={10}
                className="font-mono text-xs"
              />
            )}
            <p className="mt-1 text-xs text-muted-foreground">{t("service.usageGuideHint")}</p>
            {errors.usage_guide && (
              <p className="mt-1 text-xs text-danger">{errors.usage_guide.message}</p>
            )}
          </div>
        )}
      </section>

      <div className="flex gap-3">
        <Button type="submit" disabled={isPending}>
          {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {isPending
            ? isEdit
              ? t("common.updating")
              : t("common.creating")
            : isEdit
              ? t("service.update")
              : t("service.create")}
        </Button>
        <Button type="button" variant="secondary" onClick={() => navigate("/services")}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
