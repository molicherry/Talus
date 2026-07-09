import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Copy, Key, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";

const API_BASE = "";

interface APIKeyItem {
  id: number;
  name: string;
  key_prefix: string;
  created_at: string;
}

export function ApiKeysPage() {
  const { t } = useTranslation();
  const [keys, setKeys] = useState<APIKeyItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState("");
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

  const handleCreate = async () => {
    setCreating(true);
    try {
      const res = await fetch(`${API_BASE}/api/v1/api-keys`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: JSON.stringify({ name: newName }),
      });
      if (!res.ok) throw new Error("Failed");
      const json = await res.json();
      setNewKey(json.data.key);
      setNewName("");
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
            <div className="flex items-end gap-3">
              <div className="flex-1">
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
                className="rounded-lg p-2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
              >
                <X className="h-5 w-5" />
              </button>
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
                <th className="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {t("apiKeys.name")}
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  Prefix
                </th>
                <th className="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {t("common.created")}
                </th>
                <th className="px-4 py-3 text-right text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
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
