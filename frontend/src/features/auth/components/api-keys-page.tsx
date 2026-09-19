import { Copy, Key, Plus, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { Field, Input } from "../../../components/ui/field";
import { TBody, Table, TableCard, Td, Th, THead } from "../../../components/ui/table";
import { useTranslation } from "../../../i18n";
import { cn } from "../../../lib/utils";
import { toast } from "../../../lib/toast";

const API_BASE = "";

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

interface APIKeyItem {
  id: number;
  name: string;
  key_prefix: string;
  scopes: string[];
  server_ids?: number[];
  created_at: string;
}

interface ServerItem {
  id: number;
  name: string;
}

export function ApiKeysPage() {
  const { t } = useTranslation();
  const [keys, setKeys] = useState<APIKeyItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState("");
  const [newScopes, setNewScopes] = useState<Scope[]>([...PRESET_OPS]);
  const [servers, setServers] = useState<ServerItem[]>([]);
  const [serverMode, setServerMode] = useState<"all" | "specific">("all");
  const [newServerIDs, setNewServerIDs] = useState<number[]>([]);
  const [newKey, setNewKey] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<APIKeyItem | null>(null);
  const [deleting, setDeleting] = useState(false);

  const token = localStorage.getItem("auth_token");

  const fetchKeys = useCallback(async () => {
    setLoading(true);
    try {
      const res = await fetch(`${API_BASE}/api/v1/api-keys`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error("Failed to load API keys");
      const json = await res.json();
      setKeys(json.data ?? []);
    } catch {
      toast.error(t("common.loadError"));
    }
    setLoading(false);
  }, [token, t]);

  useEffect(() => {
    fetchKeys();
  }, [fetchKeys]);

  useEffect(() => {
    const fetchServers = async () => {
      try {
        const res = await fetch(`${API_BASE}/api/v1/servers`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!res.ok) return;
        const json = await res.json();
        setServers(json.data ?? []);
      } catch {
        /* ignore — the server selector simply stays empty */
      }
    };
    fetchServers();
  }, [token]);

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
  };

  const handleCreate = async () => {
    setCreating(true);
    try {
      const body: Record<string, unknown> = { name: newName };
      if (newScopes.length > 0) body.scopes = newScopes;
      if (serverMode === "specific" && newServerIDs.length > 0) body.server_ids = newServerIDs;
      const res = await fetch(`${API_BASE}/api/v1/api-keys`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error("Failed");
      const json = await res.json();
      setNewKey(json.data.key);
      resetCreateForm();
      fetchKeys();
    } catch {
      toast.error(t("common.error"));
    }
    setCreating(false);
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    const id = deleteTarget.id;
    setDeleting(true);
    try {
      const res = await fetch(`${API_BASE}/api/v1/api-keys/${id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error("Failed");
      await fetchKeys();
      toast.success(t("common.deleted"));
      setDeleteTarget(null);
    } catch {
      toast.error(t("apiKeys.deleteFailed"));
    }
    setDeleting(false);
  };

  const copyKey = async (key: string) => {
    try {
      await navigator.clipboard.writeText(key);
      toast.success(t("common.copied"));
    } catch {
      const el = document.createElement("textarea");
      el.value = key;
      el.style.position = "fixed";
      el.style.opacity = "0";
      document.body.appendChild(el);
      el.select();
      document.execCommand("copy");
      document.body.removeChild(el);
      toast.success(t("common.copied"));
    }
  };

  const revealKey = async (id: number) => {
    try {
      const res = await fetch(`${API_BASE}/api/v1/api-keys/${id}/reveal`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error("Failed");
      const json = await res.json();
      if (json.data) copyKey(json.data);
    } catch {
      toast.error(t("common.error"));
    }
  };

  const scopeLabel = (scope: string) => t(`apiKeys.scopes.${scope.replace(":", "_")}`, scope);
  const activePreset = detectPreset(newScopes);

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
                <Button type="button" variant="ghost" size="icon" onClick={() => copyKey(newKey)}>
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
              <button
                type="button"
                onClick={() => setShowCreate(false)}
                className="mt-3 text-sm text-primary hover:text-primary-hover"
              >
                {t("common.close")}
              </button>
            </div>
          ) : (
            <div className="space-y-5">
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
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t("apiKeys.serversAllHint")}
                  </p>
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
                <Button
                  type="button"
                  onClick={handleCreate}
                  disabled={creating || newName.trim() === ""}
                >
                  {creating ? t("common.creating") : t("apiKeys.generate")}
                </Button>
                <Button type="button" variant="ghost" onClick={() => setShowCreate(false)}>
                  {t("common.cancel")}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {loading ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-14 animate-pulse rounded-xl bg-muted/60" />
          ))}
        </div>
      ) : keys.length === 0 ? (
        <div className="rounded-2xl border border-dashed border-border bg-card p-12 text-center">
          <p className="text-muted-foreground">{t("apiKeys.empty")}</p>
        </div>
      ) : (
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
                        onClick={() => revealKey(k.id)}
                        className="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                        aria-label={t("common.copy")}
                      >
                        <Copy className="h-4 w-4" />
                      </button>
                      <button
                        type="button"
                        onClick={() => setDeleteTarget(k)}
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
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t("apiKeys.deleteTitle")}
        message={t("apiKeys.deleteMessage", { name: deleteTarget?.name || `#${deleteTarget?.id}` })}
        confirmLabel={t("common.delete")}
        isPending={deleting}
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
