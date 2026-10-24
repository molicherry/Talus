import { useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { Button } from "../../components/ui/button";
import { Input, Select } from "../../components/ui/field";
import { ConfirmDialog } from "../../components/ui/confirm-dialog";
import { StatusIndicator } from "../../components/ui/status-indicator";
import { useAuth } from "../../hooks/use-auth";
import { usePageVisibility } from "../../hooks/use-page-visibility";
import { useTranslation } from "../../i18n";
import { ApiClientError, apiClient } from "../../lib/api-client";
import { translateApiError } from "../../lib/api-error";
import { ServerSummarySchema, type ServerSummary } from "../../types/models";
import { AgentMonitoringStatus } from "../servers/components/monitoring-status";
import {
  getSettings,
  saveSettings,
  proposedValues,
  needsRetentionConfirmation,
  type Settings,
  type Setting,
  type SettingsPatch,
  type SettingValue,
} from "./api";

const groups = [
  "monitoring",
  "ssh",
  "execution",
  "retention",
  "security",
  "logging",
  "capacity",
];
const advanced = new Set([
  "AGENT_ALLOW_HTTP",
  "SSH_MAX_IDLE",
  "EXEC_TIMEOUT_MAX",
  "EXEC_OUTPUT_LIMIT",
  "EXEC_OUTPUT_STREAM_LIMIT",
  "METRICS_RETENTION_ENABLED",
  "METRICS_RETENTION_DRY_RUN",
  "LOG_FORMAT",
]);

export function SettingsPage() {
  const { isAdmin, authEpoch } = useAuth();
  const { t } = useTranslation();
  if (!isAdmin)
    return (
      <p role="alert" className="p-8">
        {t("settings.denied")}
      </p>
    );
  return <SettingsSession key={authEpoch} />;
}

function SettingsSession() {
  const { t } = useTranslation();
  const { hash } = useLocation();
  const visible = usePageVisibility();
  const [data, setData] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<SettingsPatch>({});
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [reload, setReload] = useState(0);
  const [servers, setServers] = useState<ServerSummary[]>([]);
  const [serverError, setServerError] = useState(false);
  const lifetime = useRef<AbortController | null>(null);
  const dirty = Object.keys(draft).length > 0;
  const loaded = data !== null;

  useEffect(() => {
    if (!loaded) return;
    const group = hash.slice(1);
    if (groups.includes(group)) {
      document.getElementById(group)?.scrollIntoView({ block: "start" });
    }
  }, [loaded, hash]);

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    getSettings(controller.signal)
      .then((v) => {
        if (!controller.signal.aborted) {
          setData(v);
          setDraft({});
          setError("");
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(translateApiError(e, t));
      });
    return () => controller.abort();
  }, [reload, t]);

  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let busy = false;
    const refresh = async () => {
      if (busy) return;
      busy = true;
      try {
        const response = ServerSummarySchema.array().parse(
          await apiClient.get("/api/v1/servers/summary", {
            signal: controller.signal,
          }),
        );
        if (!controller.signal.aborted) {
          setServers(response);
          setServerError(false);
        }
      } catch {
        if (!controller.signal.aborted) setServerError(true);
      } finally {
        busy = false;
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [visible]);

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const save = async (confirmed = false) => {
    if (!data || pending) return;
    const proposed = proposedValues(data.settings, draft);
    for (const setting of data.settings) {
      if (!(setting.key in draft) || draft[setting.key] === null) continue;
      const v = proposed[setting.key];
      if (
        setting.kind === "number" &&
        (typeof v !== "number" ||
          !Number.isInteger(v) ||
          v < (setting.min ?? 1) ||
          v > (setting.max ?? Infinity))
      ) {
        setError(
          t("settings.invalidField", {
            field: t(`settings.fields.${setting.key}.label`),
          }),
        );
        return;
      }
    }
    if (
      Number(proposed.EXEC_TIMEOUT) > Number(proposed.EXEC_TIMEOUT_MAX) ||
      Number(proposed.EXEC_OUTPUT_STREAM_LIMIT) >
        Number(proposed.EXEC_OUTPUT_LIMIT)
    ) {
      setError(t("settings.invalidLimits"));
      return;
    }
    if (!confirmed && needsRetentionConfirmation(data.settings, draft)) {
      setConfirm(true);
      return;
    }
    setPending(true);
    setError("");
    setNotice("");
    const signal = lifetime.current?.signal;
    try {
      const next = await saveSettings(data.revision, draft, confirmed, signal);
      if (signal?.aborted) return;
      setData({ ...next, migration: data.migration });
      setDraft({});
      setConfirm(false);
      setNotice(
        t(
          next.settings.some((s) => s.restart_required)
            ? "settings.savedRestart"
            : "settings.saved",
        ),
      );
    } catch (e) {
      if (signal?.aborted) return;
      if (
        e instanceof ApiClientError &&
        e.reason === "settings_retention_confirmation"
      )
        setConfirm(true);
      else if (e instanceof ApiClientError && e.status === 409)
        setError(t("settings.conflict"));
      else if (
        e instanceof ApiClientError &&
        e.reason === "settings_invalid" &&
        typeof e.params?.key === "string"
      ) {
        const key = e.params.key;
        const field = data.settings.some((s) => s.key === key)
          ? t(`settings.fields.${key}.label`)
          : key;
        setError(t("settings.invalidField", { field }));
      } else setError(translateApiError(e, t));
    } finally {
      if (!signal?.aborted) setPending(false);
    }
  };

  const display = (v: SettingValue) =>
    typeof v === "boolean"
      ? t(v ? "settings.enabled" : "settings.disabled")
      : v === ""
        ? t("settings.empty")
        : String(v);
  const change = (key: string, value: SettingValue | null) => {
    setDraft((old) => ({ ...old, [key]: value }));
    setNotice("");
  };
  const field = (s: Setting) => {
    const changed = s.key in draft;
    const value = changed ? (draft[s.key] ?? s.fallback_value) : s.value;
    const source = changed
      ? draft[s.key] === null
        ? s.fallback_source
        : "database"
      : s.source;
    return (
      <div
        key={s.key}
        className="space-y-2 border-b border-border py-5 last:border-0"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <label htmlFor={s.key} className="font-medium">
            {t(`settings.fields.${s.key}.label`)}
          </label>
          <span className="text-xs text-muted-foreground">
            {t(`settings.source.${source}`)}
            {changed ? ` · ${t("settings.unsaved")}` : ""}
          </span>
        </div>
        <p id={`${s.key}-help`} className="text-sm text-muted-foreground">
          {t(`settings.fields.${s.key}.help`)}
        </p>
        {s.kind === "boolean" ? (
          <Select
            id={s.key}
            aria-describedby={`${s.key}-help`}
            value={String(value)}
            onChange={(e) => change(s.key, e.target.value === "true")}
          >
            <option value="false">{t("settings.disabled")}</option>
            <option value="true">{t("settings.enabled")}</option>
          </Select>
        ) : s.choices ? (
          <Select
            id={s.key}
            aria-describedby={`${s.key}-help`}
            value={String(value)}
            onChange={(e) => change(s.key, e.target.value)}
          >
            {s.choices.map((choice) => (
              <option key={choice} value={choice}>
                {choice}
              </option>
            ))}
          </Select>
        ) : (
          <Input
            id={s.key}
            aria-describedby={`${s.key}-help`}
            type={s.kind === "number" ? "number" : "url"}
            min={s.min}
            max={s.max}
            step={s.kind === "number" ? 1 : undefined}
            value={String(value)}
            placeholder={
              s.key === "AGENT_HUB_URL" ? "https://hub.example.com" : undefined
            }
            onChange={(e) =>
              change(
                s.key,
                s.kind === "number" && e.target.value !== ""
                  ? Number(e.target.value)
                  : e.target.value,
              )
            }
          />
        )}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
          <span>{t(`settings.apply.${s.apply}`)}</span>
          <span>
            {t("settings.fallback", { value: display(s.fallback_value) })}
          </span>
          <button
            type="button"
            className="text-link underline disabled:opacity-40"
            disabled={s.source !== "database" && !changed}
            onClick={() => {
              if (s.source === "database") change(s.key, null);
              else
                setDraft((old) => {
                  const next = { ...old };
                  delete next[s.key];
                  return next;
                });
            }}
          >
            {t("settings.restore")}
          </button>
          {s.key === "AGENT_HUB_URL" && (
            <button
              type="button"
              className="text-link underline"
              onClick={() => change(s.key, window.location.origin)}
            >
              {t("settings.useOrigin")}
            </button>
          )}
        </div>
        {s.restart_required && (
          <p role="status" className="text-sm text-warning">
            {t("settings.pendingRestart", {
              value: display(s.effective_value),
            })}
          </p>
        )}
      </div>
    );
  };

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">{t("nav.settings")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {t("settings.description")}
        </p>
      </div>
      {error && (
        <p
          role="alert"
          className="rounded-lg border border-danger/30 bg-danger-subtle p-4 text-sm text-danger"
        >
          {error}
        </p>
      )}
      {notice && (
        <p
          role="status"
          className="rounded-lg border border-border p-4 text-sm"
        >
          {notice}
        </p>
      )}
      {data?.recovery && (
        <p
          role="alert"
          className="rounded-lg border border-warning/30 p-4 text-sm text-warning"
        >
          {t("settings.recovery", {
            revision: data.recovery.revision,
            key: data.recovery.key,
          })}
        </p>
      )}
      {!data ? (
        <Button type="button" onClick={() => setReload((v) => v + 1)}>
          {t(error ? "common.retry" : "common.loading")}
        </Button>
      ) : (
        <>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            <fieldset disabled={pending} className="min-w-0 space-y-6">
              {groups.map((group) => {
                const fields = data.settings.filter((s) => s.group === group);
                return (
                  <section
                    key={group}
                    id={group}
                    aria-labelledby={`settings-${group}`}
                    className="rounded-xl border border-border bg-card p-5 sm:p-6"
                  >
                    <h2
                      id={`settings-${group}`}
                      className="text-lg font-semibold"
                    >
                      {t(`settings.groups.${group}`)}
                    </h2>
                    {fields.filter((s) => !advanced.has(s.key)).map(field)}
                    {fields.some((s) => advanced.has(s.key)) && (
                      <details className="mt-3">
                        <summary className="cursor-pointer text-sm font-medium text-muted-foreground">
                          {t("settings.advanced")}
                        </summary>
                        {fields.filter((s) => advanced.has(s.key)).map(field)}
                      </details>
                    )}
                  </section>
                );
              })}
              <div className="sticky bottom-0 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card p-4 shadow-sm">
                <span className="text-sm text-muted-foreground">
                  {t(dirty ? "settings.unsaved" : "settings.upToDate")}
                </span>
                <div className="flex gap-3">
                  <Button
                    type="button"
                    variant="secondary"
                    onClick={() => {
                      setDraft({});
                      setReload((v) => v + 1);
                    }}
                  >
                    {t("settings.reload")}
                  </Button>
                  <Button type="submit" disabled={!dirty || pending}>
                    {t(pending ? "settings.saving" : "settings.save")}
                  </Button>
                </div>
              </div>
            </fieldset>
          </form>
          <section className="rounded-xl border border-border bg-card p-5 sm:p-6">
            <h2 className="text-lg font-semibold">
              {t("settings.nodeStatus")}
            </h2>
            <p className="mt-2 text-sm text-muted-foreground">
              {t("settings.nodeHelp")}
            </p>
            {serverError && (
              <p role="alert" className="mt-3 text-sm text-warning">
                {t("settings.nodeError")}
              </p>
            )}
            {!serverError && servers.length === 0 && (
              <p className="mt-3 text-sm text-muted-foreground">
                {t("settings.noNodes")}
              </p>
            )}
            {servers.map((s) => (
              <div
                key={s.id}
                className="border-b border-border py-4 last:border-0"
              >
                <div className="flex flex-wrap items-center gap-3">
                  <Link
                    to={`/servers/${s.id}`}
                    className="font-medium text-link"
                  >
                    {s.name}
                  </Link>
                  <StatusIndicator status={s.status} size="sm" showLabel />
                </div>
                <AgentMonitoringStatus status={s.monitoring} compact />
              </div>
            ))}
          </section>
          <section className="rounded-xl border border-border bg-card p-5 sm:p-6">
            <h2 className="text-lg font-semibold">
              {t("settings.migrationTitle")}
            </h2>
            <p className="mt-2 text-sm">
              {t(`settings.migration.${data.migration?.state ?? "waiting"}`)}
            </p>
            <p className="mt-2 text-sm text-muted-foreground">
              {t("settings.migrationHelp")}
            </p>
          </section>
        </>
      )}
      <ConfirmDialog
        open={confirm}
        title={t("settings.retentionTitle")}
        message={t("settings.retentionWarning")}
        confirmLabel={t("settings.save")}
        isPending={pending}
        onCancel={() => setConfirm(false)}
        onConfirm={() => void save(true)}
      />
    </div>
  );
}
