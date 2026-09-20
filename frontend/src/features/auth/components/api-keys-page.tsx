import { Copy, Key, Loader2, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";

import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { Field, Input } from "../../../components/ui/field";
import { TBody, Table, TableCard, Td, Th, THead } from "../../../components/ui/table";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import { cn } from "../../../lib/utils";
import { useServers } from "../../servers/hooks/use-servers";
import { apiKeyErrorKind } from "../lib/api-key-error";
import {
  useAPIKeys,
  useCreateAPIKey,
  useDeleteAPIKey,
  useRevealAPIKey,
} from "../hooks/use-api-keys";

const ALL_SCOPES = [
  "servers:read",
  "servers:write",
  "servers:exec",
  "servers:terminal",
  "metrics:read",
  "credentials:read",
  "services:read",
  "services:relay",
] as const;

type Scope = (typeof ALL_SCOPES)[number];

/** Read-only monitoring: can look at everything, cannot act on anything. */
const PRESET_READONLY: Scope[] = [
  "servers:read",
  "metrics:read",
  "credentials:read",
  "services:read",
];

/** Daily operations: monitoring plus command execution and terminal access. */
const PRESET_OPS: Scope[] = [
  "servers:read",
  "servers:exec",
  "servers:terminal",
  "metrics:read",
  "credentials:read",
  "services:read",
];

type PresetID = "readonly" | "ops" | "custom";

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((item) => set.has(item));
}

function detectPreset(scopes: readonly string[]): PresetID {
  if (sameSet(scopes, PRESET_READONLY)) return "readonly";
  if (sameSet(scopes, PRESET_OPS)) return "ops";
  return "custom";
}

export function ApiKeysPage() {
  const { t } = useTranslation();

  const keysQuery = useAPIKeys();
  const serversQuery = useServers();
  const createMutation = useCreateAPIKey();
  const deleteMutation = useDeleteAPIKey();
  const revealMutation = useRevealAPIKey();

  const keys = keysQuery.data;

  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState("");
  const [newScopes, setNewScopes] = useState<Scope[]>([...PRESET_OPS]);
  const [serverMode, setServerMode] = useState<"all" | "specific">("all");
  const [newServerIDs, setNewServerIDs] = useState<number[]>([]);
  const [newKey, setNewKey] = useState<string | null>(null);
  const [createError, setCreateError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; name: string } | null>(null);

  const servers = serversQuery.data ?? [];

  const errorText = (error: unknown): string => {
    switch (apiKeyErrorKind(error)) {
      case "unauthorized":
        return t("apiKeys.errorUnauthorized");
      case "forbidden":
        return t("apiKeys.errorForbidden");
      case "rateLimited":
        return t("apiKeys.errorRateLimited");
      case "server":
        return t("apiKeys.errorServer");
      case "network":
        return t("apiKeys.errorNetwork");
      default:
        return t("common.error");
    }
  };

  const toggleScope = (scope: Scope) => {
    setNewScopes((prev) =>
      prev.includes(scope) ? prev.filter((s) => s !== scope) : [...prev, scope],
    );
  };

  const applyPreset = (preset: PresetID) => {
    if (preset === "readonly") setNewScopes([...PRESET_READONLY]);
    else if (preset === "ops") setNewScopes([...PRESET_OPS]);
  };

  const toggleServer = (id: number) => {
    setNewServerIDs((prev) => (prev.includes(id) ? prev.filter((s) => s !== id) : [...prev, id]));
  };

  const resetCreateForm = () => {
    setNewName("");
    setNewScopes([...PRESET_OPS]);
    setServerMode("all");
    setNewServerIDs([]);
    setCreateError(null);
  };

  // "Specific servers" needs a usable selection: while the options are
  // unknown or still loading we must not submit, or the request would silently
  // mean "all servers".
  const serversReady =
    serverMode === "all" ||
    (!serversQuery.isLoading && !serversQuery.isError && newServerIDs.length > 0);
  const canCreate = newName.trim() !== "" && !createMutation.isPending && serversReady;

  const handleCreate = () => {
    setCreateError(null);
    const input = {
      name: newName,
      scopes: newScopes,
      ...(serverMode === "specific" ? { server_ids: newServerIDs } : {}),
    };
    createMutation.mutate(input, {
      onSuccess: (result) => {
        setNewKey(result.key);
        resetCreateForm();
      },
      onError: (error) => setCreateError(errorText(error)),
    });
  };

  const handleDelete = () => {
    if (!deleteTarget) return;
    const { id } = deleteTarget;
    deleteMutation.mutate(id, {
      onSuccess: () => {
        toast.success(t("common.deleted"));
        setDeleteTarget(null);
      },
      onError: (error) => {
        // Keep the row: nothing was removed server-side.
        toast.error(errorText(error));
        setDeleteTarget(null);
      },
    });
  };

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

  const handleReveal = (id: number) => {
    revealMutation.mutate(id, {
      onSuccess: (raw) => {
        if (raw) void copyToClipboard(raw);
      },
      onError: (error) => toast.error(errorText(error)),
    });
  };

  const scopeLabel = (scope: string) => t(`apiKeys.scopes.${scope.replace(":", "_")}`, scope);
  const activePreset = detectPreset(newScopes);

  const showFirstLoadError = keysQuery.isError && keys === undefined && !keysQuery.isLoading;
  const showRefreshError = keysQuery.isError && keys !== undefined;
  const showEmpty = keys !== undefined && keys.length === 0 && !keysQuery.isError;

  return (
    <div>
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h1 className="inline-flex items-center gap-2 text-2xl font-semibold text-foreground">
          <Key className="h-6 w-6" />
          {t("apiKeys.title")}
        </h1>
        <Button
          type="button"
          onClick={() => {
            setShowCreate(true);
            setNewKey(null);
            setCreateError(null);
          }}
        >
          <Plus className="h-4 w-4" />
          {t("apiKeys.create")}
        </Button>
      </div>

      {showCreate && (
        <div className="mb-6 rounded-2xl border border-border bg-card p-6 shadow-card">
          {newKey ? (
            <div>
              <p className="text-sm font-medium text-success">{t("apiKeys.created")}</p>
              <div className="mt-2 flex items-center gap-2">
                <code className="flex-1 break-all rounded-lg bg-muted px-3 py-2 font-mono text-sm text-foreground">
                  {newKey}
                </code>
                <Button type="button" variant="ghost" size="icon" onClick={() => copyToClipboard(newKey)}>
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
              <button
                type="button"
                onClick={() => {
                  setShowCreate(false);
                  setNewKey(null);
                }}
                className="mt-3 text-sm text-primary hover:text-primary-hover"
              >
                {t("common.close")}
              </button>
            </div>
          ) : (
            <div className="space-y-5">
              {createError && (
                <div className="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger">
                  {createError}
                </div>
              )}

              <Field label={t("apiKeys.name")} htmlFor="api-key-name">
                <Input
                  id="api-key-name"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder={t("apiKeys.namePlaceholder")}
                />
              </Field>

              <div>
                <span className="mb-1 block text-sm font-medium text-foreground">
                  {t("apiKeys.presetsLabel")}
                </span>
                <div className="flex flex-wrap gap-2">
                  {(["readonly", "ops", "custom"] as PresetID[]).map((preset) => {
                    const active = activePreset === preset;
                    const label =
                      preset === "readonly"
                        ? t("apiKeys.presetReadonly")
                        : preset === "ops"
                          ? t("apiKeys.presetOps")
                          : t("apiKeys.presetCustom");
                    return (
                      <button
                        key={preset}
                        type="button"
                        disabled={preset === "custom"}
                        onClick={() => applyPreset(preset)}
                        className={cn(
                          "rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors",
                          active
                            ? "border-primary bg-primary-subtle text-primary"
                            : "border-border bg-card text-muted-foreground hover:border-border-hover",
                          preset === "custom" && "cursor-default",
                        )}
                      >
                        {label}
                      </button>
                    );
                  })}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  {activePreset === "readonly"
                    ? t("apiKeys.presetReadonlyHint")
                    : activePreset === "ops"
                      ? t("apiKeys.presetOpsHint")
                      : ""}
                </p>
              </div>

              <div>
                <span className="mb-1 block text-sm font-medium text-foreground">
                  {t("apiKeys.scopesLabel")}
                </span>
                <div className="flex flex-wrap gap-2">
                  {ALL_SCOPES.map((scope) => {
                    const selected = newScopes.includes(scope);
                    return (
                      <label
                        key={scope}
                        className={cn(
                          "inline-flex cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors",
                          selected
                            ? "border-primary bg-primary-subtle text-primary"
                            : "border-border bg-card text-muted-foreground hover:border-border-hover",
                        )}
                      >
                        <input
                          type="checkbox"
                          checked={selected}
                          onChange={() => toggleScope(scope)}
                          className="h-3.5 w-3.5 rounded border-input accent-primary"
                        />
                        {scopeLabel(scope)}
                      </label>
                    );
                  })}
                </div>
              </div>

              <div>
                <span className="mb-1 block text-sm font-medium text-foreground">
                  {t("apiKeys.serversScopeLabel")}
                </span>
                <div className="flex flex-wrap gap-2">
                  {(["all", "specific"] as const).map((mode) => (
                    <button
                      key={mode}
                      type="button"
                      onClick={() => setServerMode(mode)}
                      className={cn(
                        "rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors",
                        serverMode === mode
                          ? "border-primary bg-primary-subtle text-primary"
                          : "border-border bg-card text-muted-foreground hover:border-border-hover",
                      )}
                    >
                      {mode === "all"
                        ? t("apiKeys.serversAllOption")
                        : t("apiKeys.serversSpecificOption")}
                    </button>
                  ))}
                </div>
                {serverMode === "all" ? (
                  <p className="mt-1 text-xs text-muted-foreground">{t("apiKeys.serversAllHint")}</p>
                ) : serversQuery.isLoading ? (
                  <p className="mt-1 text-xs text-muted-foreground">{t("common.loading")}</p>
                ) : serversQuery.isError ? (
                  <div className="mt-1 flex items-center gap-2 text-xs text-danger">
                    <span>
                      {t("apiKeys.serversLoadFailed")} — {errorText(serversQuery.error)}
                    </span>
                    <button
                      type="button"
                      onClick={() => serversQuery.refetch()}
                      className="inline-flex items-center gap-1 font-medium text-primary hover:text-primary-hover"
                    >
                      <RefreshCw className="h-3 w-3" />
                      {t("common.retry")}
                    </button>
                  </div>
                ) : servers.length === 0 ? (
                  <p className="mt-1 text-xs text-muted-foreground">{t("apiKeys.empty")}</p>
                ) : (
                  <>
                    <div className="mt-2 flex flex-wrap gap-2">
                      {servers.map((srv) => {
                        const selected = newServerIDs.includes(srv.id);
                        return (
                          <label
                            key={srv.id}
                            className={cn(
                              "inline-flex cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors",
                              selected
                                ? "border-primary bg-primary-subtle text-primary"
                                : "border-border bg-card text-muted-foreground hover:border-border-hover",
                            )}
                          >
                            <input
                              type="checkbox"
                              checked={selected}
                              onChange={() => toggleServer(srv.id)}
                              className="h-3.5 w-3.5 rounded border-input accent-primary"
                            />
                            {srv.name}
                          </label>
                        );
                      })}
                    </div>
                    <p
                      className={cn(
                        "mt-1 text-xs",
                        newServerIDs.length === 0 ? "text-warning" : "text-muted-foreground",
                      )}
                    >
                      {newServerIDs.length === 0
                        ? t("apiKeys.serversNoneSelected")
                        : t("apiKeys.serversSelected", { count: newServerIDs.length })}
                    </p>
                  </>
                )}
              </div>

              <div className="rounded-lg border border-border bg-muted/40 p-3">
                <p className="text-xs font-medium text-foreground">{t("apiKeys.summaryLabel")}</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {newScopes.length === 0
                    ? t("apiKeys.summaryNoScopes")
                    : t("apiKeys.summaryScopes", { count: newScopes.length })}
                  {" · "}
                  {serverMode === "all"
                    ? t("apiKeys.summaryServersAll")
                    : t("apiKeys.summaryServersCount", { count: newServerIDs.length })}
                </p>
                {newScopes.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {newScopes.map((scope) => (
                      <span
                        key={scope}
                        className="inline-flex items-center rounded-full bg-secondary px-2 py-0.5 text-[11px] font-medium text-secondary-foreground"
                      >
                        {scopeLabel(scope)}
                      </span>
                    ))}
                  </div>
                )}
              </div>

              <div className="flex gap-3">
                <Button type="button" onClick={handleCreate} disabled={!canCreate}>
                  {createMutation.isPending ? t("common.creating") : t("apiKeys.generate")}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => {
                    setShowCreate(false);
                    setCreateError(null);
                  }}
                >
                  {t("common.cancel")}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {keysQuery.isLoading && keys === undefined ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-14 animate-pulse rounded-xl bg-muted/60" />
          ))}
        </div>
      ) : showFirstLoadError ? (
        <div className="rounded-2xl border border-danger/30 bg-danger-subtle p-8 text-center">
          <p className="text-sm text-danger">
            {t("apiKeys.loadFailed")} — {errorText(keysQuery.error)}
          </p>
          <Button
            type="button"
            variant="outline"
            className="mt-3"
            onClick={() => keysQuery.refetch()}
            disabled={keysQuery.isFetching}
          >
            {keysQuery.isFetching ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <RefreshCw className="h-4 w-4" />
            )}
            {t("common.retry")}
          </Button>
        </div>
      ) : showEmpty ? (
        <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
          <p className="text-muted-foreground">{t("apiKeys.empty")}</p>
        </div>
      ) : keys ? (
        <>
          {showRefreshError && (
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-warning/30 bg-warning-subtle px-4 py-2 text-sm text-warning">
              <span>
                {t("apiKeys.refreshFailed")} — {errorText(keysQuery.error)}
              </span>
              <button
                type="button"
                onClick={() => keysQuery.refetch()}
                className="inline-flex items-center gap-1 font-medium hover:underline"
              >
                <RefreshCw className="h-3.5 w-3.5" />
                {t("common.retry")}
              </button>
            </div>
          )}
          <TableCard>
            <Table>
              <THead>
                <tr>
                  <Th>{t("apiKeys.name")}</Th>
                  <Th>{t("apiKeys.scopesLabel")}</Th>
                  <Th>{t("apiKeys.serversLabel")}</Th>
                  <Th>{t("apiKeys.prefix")}</Th>
                  <Th>{t("common.created")}</Th>
                  <Th align="right">{t("common.actions")}</Th>
                </tr>
              </THead>
              <TBody>
                {keys.map((k) => (
                  <tr key={k.id} className="transition-colors hover:bg-muted/40">
                    <Td className="font-medium text-foreground">{k.name || `#${k.id}`}</Td>
                    <Td>
                      <div className="flex flex-wrap gap-1">
                        {(k.scopes ?? []).map((scope) => (
                          <span
                            key={scope}
                            className="inline-flex items-center rounded-full bg-secondary px-2 py-0.5 text-xs font-medium text-secondary-foreground"
                          >
                            {scopeLabel(scope)}
                          </span>
                        ))}
                      </div>
                    </Td>
                    <Td className="text-muted-foreground">
                      {k.server_ids && k.server_ids.length > 0
                        ? t("apiKeys.serversCount", { count: k.server_ids.length })
                        : t("apiKeys.serversAll")}
                    </Td>
                    <Td className="font-mono text-muted-foreground">{k.key_prefix}...</Td>
                    <Td className="text-muted-foreground">
                      {new Date(k.created_at).toLocaleDateString()}
                    </Td>
                    <Td>
                      <div className="flex items-center justify-end gap-1">
                        <button
                          type="button"
                          onClick={() => handleReveal(k.id)}
                          disabled={revealMutation.isPending}
                          className="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:opacity-50"
                          aria-label={t("common.copy")}
                        >
                          <Copy className="h-4 w-4" />
                        </button>
                        <button
                          type="button"
                          onClick={() => setDeleteTarget({ id: k.id, name: k.name })}
                          className="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
                          aria-label={t("common.delete")}
                        >
                          <Trash2 className="h-4 w-4" />
                        </button>
                      </div>
                    </Td>
                  </tr>
                ))}
              </TBody>
            </Table>
          </TableCard>
        </>
      ) : null}

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t("apiKeys.deleteTitle")}
        message={t("apiKeys.deleteMessage", {
          name: deleteTarget?.name || `#${deleteTarget?.id}`,
        })}
        confirmLabel={t("common.delete")}
        isPending={deleteMutation.isPending}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
