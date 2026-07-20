import { Copy, Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface KeyValueHint {
  key: string;
  value: string;
  hint: string;
}

interface ServiceKeyInputProps {
  value: { credentials: Record<string, string>; hints: Record<string, string> };
  onChange: (value: { credentials: Record<string, string>; hints: Record<string, string> }) => void;
}

function toRows(value: { credentials: Record<string, string>; hints: Record<string, string> }): KeyValueHint[] {
  const rows: KeyValueHint[] = [];
  for (const key of Object.keys(value.credentials)) {
    rows.push({ key, value: value.credentials[key] || "", hint: value.hints[key] || "" });
  }
  return rows;
}

function fromRows(rows: KeyValueHint[]): { credentials: Record<string, string>; hints: Record<string, string> } {
  const credentials: Record<string, string> = {};
  const hints: Record<string, string> = {};
  for (const row of rows) {
    credentials[row.key] = row.value;
    hints[row.key] = row.hint;
  }
  return { credentials, hints };
}

const copyToClipboard = (text: string): Promise<void> => {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text).then(() => {});
  }
  const el = document.createElement("textarea");
  el.value = text;
  el.style.position = "fixed";
  el.style.opacity = "0";
  document.body.appendChild(el);
  el.select();
  document.execCommand("copy");
  document.body.removeChild(el);
  return Promise.resolve();
};

export function ServiceKeyInput({ value, onChange }: ServiceKeyInputProps) {
  const { t } = useTranslation();
  const [visibleValues, setVisibleValues] = useState<Record<number, boolean>>({});
  const rows = toRows(value);

  const updateRow = (index: number, field: "key" | "value" | "hint", newVal: string) => {
    const updated = rows.map((r, i) => (i === index ? { ...r, [field]: newVal } : r));
    onChange(fromRows(updated));
  };

  const addRow = () => {
    onChange(fromRows([...rows, { key: "", value: "", hint: "" }]));
  };

  const removeRow = (index: number) => {
    onChange(fromRows(rows.filter((_, i) => i !== index)));
  };

  const toggleVisible = (index: number) => {
    setVisibleValues((prev) => ({ ...prev, [index]: !prev[index] }));
  };

  const handleCopy = (text: string) => {
    copyToClipboard(text).then(() => toast.success(t("common.copied")));
  };

  return (
    <div className="space-y-3">
      {rows.length > 0 && (
        <div className="grid grid-cols-[1fr_1fr_1fr_auto] gap-2 text-xs font-medium text-gray-500 dark:text-gray-400">
          <span>{t("service.key")}</span>
          <span>{t("service.value")}</span>
          <span>{t("service.hint")}</span>
          <span />
        </div>
      )}
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_1fr_1fr_auto] gap-2">
          <input
            type="text"
            value={row.key}
            onChange={(e) => updateRow(index, "key", e.target.value)}
            placeholder="token"
            className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          <div className="relative">
            <input
              type={visibleValues[index] ? "text" : "password"}
              value={row.value}
              onChange={(e) => updateRow(index, "value", e.target.value)}
              placeholder="ptr_xxx"
              className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 pr-16 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
            />
            <div className="absolute right-0 top-0 flex h-full items-center gap-0.5 pr-1">
              <button
                type="button"
                onClick={() => handleCopy(row.value)}
                className="cursor-pointer rounded p-1 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-gray-700 dark:hover:text-gray-300"
                aria-label={t("common.copied")}
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
              <button
                type="button"
                onClick={() => toggleVisible(index)}
                className="cursor-pointer rounded p-1 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-gray-700 dark:hover:text-gray-300"
                aria-label={visibleValues[index] ? t("service.hide") : t("service.show")}
              >
                {visibleValues[index] ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
              </button>
            </div>
          </div>
          <input
            type="text"
            value={row.hint}
            onChange={(e) => updateRow(index, "hint", e.target.value)}
            placeholder={t("service.hintPlaceholder")}
            className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          <button
            type="button"
            onClick={() => removeRow(index)}
            className="cursor-pointer rounded p-2 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-900/20"
            aria-label={t("service.removeKey")}
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={addRow}
        className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-dashed border-gray-300 px-3 py-2 text-sm text-gray-500 transition-colors hover:border-indigo-400 hover:bg-indigo-50 hover:text-indigo-600 dark:border-gray-600 dark:text-gray-400 dark:hover:border-indigo-500 dark:hover:bg-indigo-900/20 dark:hover:text-indigo-400"
      >
        <Plus className="h-4 w-4" />
        {t("service.addKey")}
      </button>
    </div>
  );
}
