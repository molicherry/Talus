import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

interface KeyValueHint {
  id: number;
  key: string;
  value: string;
  hint: string;
}

interface ServiceKeyInputProps {
  value: { credentials: Record<string, string>; hints: Record<string, string> };
  onChange: (value: { credentials: Record<string, string>; hints: Record<string, string> }) => void;
}

let nextId = 0;

function toRows(value: { credentials: Record<string, string>; hints: Record<string, string> }): KeyValueHint[] {
  const rows: KeyValueHint[] = [];
  for (const key of Object.keys(value.credentials)) {
    rows.push({ id: nextId++, key, value: value.credentials[key] || "", hint: value.hints[key] || "" });
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

export function ServiceKeyInput({ value, onChange }: ServiceKeyInputProps) {
  const { t } = useTranslation();
  const rows = toRows(value);

  const updateRow = (index: number, field: "key" | "value" | "hint", newVal: string) => {
    const updated = rows.map((r, i) => (i === index ? { ...r, [field]: newVal } : r));
    onChange(fromRows(updated));
  };

  const addRow = () => {
    onChange(fromRows([...rows, { id: nextId++, key: "", value: "", hint: "" }]));
  };

  const removeRow = (index: number) => {
    onChange(fromRows(rows.filter((_, i) => i !== index)));
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
        <div key={row.id} className="grid grid-cols-[1fr_1fr_1fr_auto] gap-2">
          <input
            type="text"
            value={row.key}
            onChange={(e) => updateRow(index, "key", e.target.value)}
            placeholder="token"
            className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
          <input
            type="text"
            value={row.value}
            onChange={(e) => updateRow(index, "value", e.target.value)}
            placeholder="ptr_xxx"
            className="w-full rounded-lg border border-gray-200 bg-white px-3 py-2 text-sm text-gray-900 placeholder:text-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100 dark:placeholder:text-gray-500"
          />
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
