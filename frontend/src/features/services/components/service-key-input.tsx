import { Copy, Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { useState } from "react";

import { Input } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";

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

const copyToClipboard = async (text: string): Promise<void> => {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const el = document.createElement("textarea");
    el.value = text;
    el.style.position = "fixed";
    el.style.opacity = "0";
    document.body.appendChild(el);
    el.select();
    document.execCommand("copy");
    document.body.removeChild(el);
  }
};

const ROW_GRID = "grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_1fr_auto]";

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

  const handleCopy = async (text: string) => {
    await copyToClipboard(text);
    toast.success(t("common.copied"));
  };

  return (
    <div className="space-y-3">
      {rows.length > 0 && (
        <div className={`${ROW_GRID} hidden text-xs font-medium text-muted-foreground sm:grid`}>
          <span>{t("service.key")}</span>
          <span>{t("service.value")}</span>
          <span>{t("service.hint")}</span>
          <span />
        </div>
      )}
      {rows.map((row, index) => (
        <div key={index} className={ROW_GRID}>
          <Input
            type="text"
            value={row.key}
            onChange={(e) => updateRow(index, "key", e.target.value)}
            placeholder="token"
          />
          <div className="relative">
            <Input
              type={visibleValues[index] ? "text" : "password"}
              value={row.value}
              onChange={(e) => updateRow(index, "value", e.target.value)}
              placeholder="ptr_xxx"
              className="pr-16"
            />
            <div className="absolute right-0 top-0 flex h-full items-center gap-0.5 pr-1">
              <button
                type="button"
                onClick={() => handleCopy(row.value)}
                className="cursor-pointer rounded p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                aria-label={t("common.copy")}
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
              <button
                type="button"
                onClick={() => toggleVisible(index)}
                className="cursor-pointer rounded p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                aria-label={visibleValues[index] ? t("service.hide") : t("service.show")}
              >
                {visibleValues[index] ? (
                  <EyeOff className="h-3.5 w-3.5" />
                ) : (
                  <Eye className="h-3.5 w-3.5" />
                )}
              </button>
            </div>
          </div>
          <Input
            type="text"
            value={row.hint}
            onChange={(e) => updateRow(index, "hint", e.target.value)}
            placeholder={t("service.hintPlaceholder")}
          />
          <button
            type="button"
            onClick={() => removeRow(index)}
            className="cursor-pointer justify-self-end rounded p-2 text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
            aria-label={t("service.removeKey")}
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={addRow}
        className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-dashed border-border px-3 py-2 text-sm text-muted-foreground transition-colors hover:border-primary hover:bg-primary-subtle hover:text-primary"
      >
        <Plus className="h-4 w-4" />
        {t("service.addKey")}
      </button>
    </div>
  );
}
