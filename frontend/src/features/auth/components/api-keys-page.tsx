import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Copy, Key, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

const API_BASE = "";

const ALL_SCOPES = [
  "servers:read",
  "servers:write",
  "servers:exec",
  "servers:terminal",
  "metrics:read",
  "credentials:read",
  "services:relay",
];

const DEFAULT_SCOPES = [
  "servers:read",
  "servers:exec",
  "servers:terminal",
  "metrics:read",
  "credentials:read",
];

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
  const [newScopes, setNewScopes] = useState<string[]>([...DEFAULT_SCOPES]);
  const [servers, setServers] = useState<ServerItem[]>([]);
  const [newServerIDs, setNewServerIDs] = useState<number[]>([]);
  const [newKey, setNewKey] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const token = localStorage.getItem("auth_token");

  const fetchKeys = async () => {
    setLoading(true);
    try {
      const res = await fetch(`${API_BASE}/api/v1/api-keys`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const json = await res.json();
      setKeys(json.data ?? []);
    } catch {
      toast.error(t("common.loadError"));
    }
    setLoading(false);
  };

  useState(() => { fetchKeys(); });

  useEffect(() => {
    const fetchServers = async () => {
      try {
        const res = await fetch(`${API_BASE}/api/v1/servers`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        const json = await res.json();
        setServers(json.data ?? []);
      } catch { /* ignore */ }
    };
    fetchServers();
  }, [token]);

  const toggleScope = (scope: string) => {
    setNewScopes((prev) =>
      prev.includes(scope) ? prev.filter((s) => s !== scope) : [...prev, scope],
    );
  };

  const toggleServer = (id: number) => {
    setNewServerIDs((prev) =>
      prev.includes(id) ? prev.filter((s) => s !== id) : [...prev, id],
    );
  };

  const handleCreate = async () => {
    setCreating(true);
    try {
      const body: Record<string, unknown> = { name: newName };
      if (newScopes.length > 0) body.scopes = newScopes;
      if (newServerIDs.length > 0) body.server_ids = newServerIDs;
      const res = await fetch(`${API_BASE}/api/v1/api-keys`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error("Failed");
      const json = await res.json();
      setNewKey(json.data.key);
      setNewName("");
      setNewScopes([...DEFAULT_SCOPES]);
      setNewServerIDs([]);
      fetchKeys();
    } catch {
      toast.error(t("common.error"));
    }
    setCreating(false);
  };

  const handleDelete = async (id: number) => {
    try {
      await fetch(`${API_BASE}/api/v1/api-keys/${id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      fetchKeys();
      toast.success(t("common.deleted"));
    } catch {
      toast.error(t("common.error"));
    }
  };

  const copyKey = (key: string) => {
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(key).then(() => toast.success(t("common.copied")));
    } else {
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

  const scopeLabel = (scope: string) => t(`apiKeys.scopes.${scope.replace(":", "_")}`, scope);

  return (
    <div>
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">
          <Key className="mr-2 inline h-6 w-6" />
          {t("apiKeys.title")}
        </h1>
        <button
          type="button"
          onClick={() => { setShowCreate(true); setNewKey(null); }}
          className="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-700"
        >
          <Plus className="h-4 w-4" />
          {t("apiKeys.create")}
        </button>
      </div>

      {showCreate && (
        <div className="mb-6 rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
          {newKey ? (
            <div>
              <p className="text-sm font-medium text-green-600 dark:text-green-400">
                {t("apiKeys.created")}
              </p>
              <div className="mt-2 flex items-center gap-2">
                <code className="flex-1 break-all rounded bg-gray-100 px-3 py-2 font-mono text-sm text-gray-900 dark:bg-gray-800 dark:text-gray-100">
                  {newKey}
                </code>
                <button
                  type="button"
                  onClick={() => copyKey(newKey)}
                  className="rounded-lg p-2 text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-gray-700"
                >
                  <Copy className="h-4 w-4" />
                </button>
              </div>
              <button
                type="button"
                onClick={() => setShowCreate(false)}
                className="mt-3 text-sm text-indigo-600 hover:text-indigo-700 dark:text-indigo-400"
              >
                {t("common.close")}
              </button>
            </div>
          ) : (
            <div className="space-y-4">
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
                  {t("apiKeys.name")}
                </label>
                <input
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
                  placeholder={t("apiKeys.namePlaceholder")}
                />
              </div>
              <div>
                <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
                  {t("apiKeys.scopesLabel")}
                </span>
                <div className="flex flex-wrap gap-2">
                  {ALL_SCOPES.map((scope) => (
                    <label
                      key={scope}
                      className={`inline-flex cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors ${
                        newScopes.includes(scope)
                          ? "border-indigo-300 bg-indigo-50 text-indigo-700 dark:border-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300"
                          : "border-gray-200 bg-white text-gray-500 hover:border-gray-300 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-400 dark:hover:border-gray-600"
                      }`}
                    >
                      <input
                        type="checkbox"
                        checked={newScopes.includes(scope)}
                        onChange={() => toggleScope(scope)}
                        className="h-3.5 w-3.5 rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"
                      />
                      {scopeLabel(scope)}
                    </label>
                  ))}
                </div>
              </div>
              {servers.length > 0 && (
                <div>
                  <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
                    {t("apiKeys.serversLabel")}
                  </span>
                  <div className="flex flex-wrap gap-2">
                    {servers.map((srv) => (
                      <label
                        key={srv.id}
                        className={`inline-flex cursor-pointer items-center gap-1.5 rounded-lg border px-2.5 py-1.5 text-xs font-medium transition-colors ${
                          newServerIDs.includes(srv.id)
                            ? "border-indigo-300 bg-indigo-50 text-indigo-700 dark:border-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300"
                            : "border-gray-200 bg-white text-gray-500 hover:border-gray-300 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-400 dark:hover:border-gray-600"
                        }`}
                      >
                        <input
                          type="checkbox"
                          checked={newServerIDs.includes(srv.id)}
                          onChange={() => toggleServer(srv.id)}
                          className="h-3.5 w-3.5 rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"
                        />
                        {srv.name}
                      </label>
                    ))}
                  </div>
                  {newServerIDs.length === 0 && (
                    <p className="mt-1 text-xs text-gray-400 dark:text-gray-500">
                      {t("apiKeys.serversAll")}
                    </p>
                  )}
                </div>
              )}
              <div className="flex gap-3">
                <button
                  type="button"
                  onClick={handleCreate}
                  disabled={creating}
                  className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
                >
                  {creating ? t("common.creating") : t("apiKeys.generate")}
                </button>
                <button
                  type="button"
                  onClick={() => setShowCreate(false)}
                  className="rounded-lg px-4 py-2 text-sm font-medium text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-300"
                >
                  {t("common.cancel")}
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {loading ? (
        <div className="space-y-3">
          {[1, 2, 3].map((i) => (
            <div key={i} className="h-14 animate-pulse rounded-lg bg-gray-100/50 dark:bg-gray-800/50" />
          ))}
        </div>
      ) : keys.length === 0 ? (
        <div className="rounded-lg border border-gray-200 p-12 text-center dark:border-gray-800">
          <p className="text-gray-500 dark:text-gray-400">{t("apiKeys.empty")}</p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-gray-200 dark:border-gray-800">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50 dark:border-gray-800 dark:bg-gray-900/50">
                <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  {t("apiKeys.name")}
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  {t("apiKeys.scopesLabel")}
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  {t("apiKeys.serversLabel")}
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  Prefix
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  {t("common.created")}
                </th>
                <th className="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                  {t("common.actions")}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
              {keys.map((k) => (
                <tr key={k.id} className="hover:bg-gray-100/50 dark:hover:bg-gray-800/50">
                  <td className="px-4 py-3 text-sm font-medium text-gray-900 dark:text-gray-100">
                    {k.name || `#${k.id}`}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex flex-wrap gap-1">
                      {(k.scopes ?? []).map((scope) => (
                        <span
                          key={scope}
                          className="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-gray-800 dark:text-gray-400"
                        >
                          {scopeLabel(scope)}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">
                    {k.server_ids && k.server_ids.length > 0
                      ? t("apiKeys.serversCount", { count: k.server_ids.length })
                      : t("apiKeys.serversAll")}
                  </td>
                  <td className="px-4 py-3 font-mono text-sm text-gray-500 dark:text-gray-400">
                    {k.key_prefix}...
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">
                    {new Date(k.created_at).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex items-center justify-end gap-1">
                      <button
                        type="button"
                        onClick={() => handleDelete(k.id)}
                        className="rounded-lg p-2 text-gray-500 hover:bg-red-50 hover:text-red-600 dark:text-gray-400 dark:hover:bg-red-900/30 dark:hover:text-red-400"
                      >
                        <Trash2 className="h-4 w-4" />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
