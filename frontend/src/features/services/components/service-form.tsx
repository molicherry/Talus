import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, Loader2 } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useNavigate } from "react-router-dom";
import { z } from "zod";

import { useSecret } from "../../../lib/use-secret";
import { SecretLoadState } from "../../../components/ui/secret-load-state";
import { translateApiError } from "../../../lib/api-error";
import { Button } from "../../../components/ui/button";
import { Field, Input, Label, Select, Textarea } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import { type Service, ServiceFormSchema, type ServiceFormValues } from "../../../types/models";
import { useServers } from "../../servers/hooks/use-servers";
import { useCreateService, useUpdateService } from "../hooks/use-services";
import { ServiceKeyInput } from "./service-key-input";
import {
  credentialRowsFromValues,
  credentialRowsToValues,
  type CredentialRow,
  type CredentialRowsError,
} from "../lib/credential-rows";

const MetadataSchema = ServiceFormSchema.omit({ credentials: true, credential_hints: true });
const SecretSchema = z.record(z.string(), z.string());
type MetadataValues = z.infer<typeof MetadataSchema>;

interface ServiceFormProps {
  service?: Service;
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="border-b border-border pb-2 text-sm font-semibold text-foreground">
      {children}
    </h3>
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
  // The keyed editor keeps its initial snapshot when metadata is refetched.
  const [initialHints] = useState(service?.credential_hints ?? {});
  const [rows, setRows] = useState<CredentialRow[]>([]);
  const [rowsError, setRowsError] = useState<CredentialRowsError | null>(null);
  const secrets = useSecret(
    service ? `/api/v1/services/${service.id}/credentials` : null,
    SecretSchema.parse,
  );
  const secretsReady = !service || secrets.data !== undefined;

  const {
    register,
    handleSubmit,
    control,
    formState: { errors },
  } = useForm<MetadataValues>({
    resolver: zodResolver(MetadataSchema),
    defaultValues: service
      ? {
          name: service.name,
          display_name: service.display_name,
          base_url: service.base_url,
          description: service.description ?? "",
          usage_guide: service.usage_guide ?? "",
          server_id: service.server_id ?? undefined,
        }
      : {},
  });

  const guideValue = useWatch({ control, name: "usage_guide" }) ?? "";

  useEffect(() => {
    if (secrets.data) setRows(credentialRowsFromValues(secrets.data, initialHints));
  }, [secrets.data, initialHints]);

  const onSubmit = (metadata: MetadataValues) => {
    if (!secretsReady || activeMutation.isPending) return;
    const result = credentialRowsToValues(rows);
    if (!result.ok) {
      setRowsError(result.error);
      if (result.error.rowId)
        document
          .getElementById(
            `${result.error.rowId}-${result.error.code === "missingValue" ? "value" : "key"}`,
          )
          ?.focus();
      else document.getElementById("service-add-key")?.focus();
      return;
    }
    const data: ServiceFormValues = {
      ...metadata,
      credentials: result.credentials,
      credential_hints: result.credential_hints,
    };
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

  const errorMessage = activeMutation.error ? translateApiError(activeMutation.error, t) : null;

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

        <Field
          label={t("service.description")}
          htmlFor="description"
          error={errors.description?.message}
        >
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
          <SecretLoadState
            isLoading={secrets.isLoading}
            error={secrets.error}
            onRetry={secrets.retry}
          />
          <ServiceKeyInput
            rows={rows}
            disabled={!secretsReady || isPending}
            error={rowsError}
            onChange={(next) => {
              setRows(next);
              setRowsError(null);
            }}
          />
          {isEdit && (
            <p className="mt-1 text-xs text-muted-foreground">{t("service.credentialsEditNote")}</p>
          )}
          {rowsError && (
            <p id="service-credentials-error" role="alert" className="mt-1 text-xs text-danger">
              {t(`service.keyErrors.${rowsError.code}`)}
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
                className="text-xs font-medium text-link transition-colors hover:text-link-hover"
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
        <Button type="submit" disabled={isPending || !secretsReady}>
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
