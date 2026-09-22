import { Copy, Eye, EyeOff, Plus, Trash2 } from "lucide-react";
import { useState } from "react";

import { Input } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { toast } from "../../../lib/toast";
import {
  createCredentialRow,
  type CredentialRow,
  type CredentialRowsError,
} from "../lib/credential-rows";

interface ServiceKeyInputProps {
  rows: CredentialRow[];
  onChange: (rows: CredentialRow[]) => void;
  disabled?: boolean;
  error?: CredentialRowsError | null;
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

export function ServiceKeyInput({ rows, onChange, disabled, error }: ServiceKeyInputProps) {
  const { t } = useTranslation();
  const [visibleValues, setVisibleValues] = useState<Record<string, boolean>>({});

  const updateRow = (id: string, field: "key" | "value" | "hint", newVal: string) => {
    onChange(rows.map((r) => (r.id === id ? { ...r, [field]: newVal } : r)));
  };

  const addRow = () => {
    onChange([...rows, createCredentialRow()]);
  };

  const removeRow = (id: string) => {
    onChange(rows.filter((r) => r.id !== id));
  };

  const toggleVisible = (id: string) => {
    setVisibleValues((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  const handleCopy = async (text: string) => {
    await copyToClipboard(text);
    toast.success(t("common.copied"));
  };

  return (
    <fieldset
      disabled={disabled}
      aria-label={t("service.credentials")}
      className="min-w-0 space-y-3 disabled:opacity-50"
    >
      {rows.length > 0 && (
        <div className={`${ROW_GRID} hidden text-xs font-medium text-muted-foreground sm:grid`}>
          <span>{t("service.key")}</span>
          <span>{t("service.value")}</span>
          <span>{t("service.hint")}</span>
          <span />
        </div>
      )}
      {rows.map((row, index) => (
        <div key={row.id} className={ROW_GRID}>
          <Input
            id={`${row.id}-key`}
            type="text"
            value={row.key}
            aria-label={`${t("service.key")} ${index + 1}`}
            aria-invalid={(error?.rowId === row.id && error.code !== "missingValue") || undefined}
            aria-describedby={error?.rowId === row.id ? "service-credentials-error" : undefined}
            onChange={(e) => updateRow(row.id, "key", e.target.value)}
            placeholder="token"
          />
          <div className="relative">
            <Input
              id={`${row.id}-value`}
              type={visibleValues[row.id] ? "text" : "password"}
              value={row.value}
              aria-label={`${t("service.value")} ${index + 1}`}
              aria-invalid={(error?.rowId === row.id && error.code === "missingValue") || undefined}
              aria-describedby={error?.rowId === row.id ? "service-credentials-error" : undefined}
              onChange={(e) => updateRow(row.id, "value", e.target.value)}
              placeholder="ptr_xxx"
              className="min-h-11 pr-24 sm:min-h-0 sm:pr-16"
            />
            <div className="absolute right-0 top-0 flex h-full items-center gap-0.5 pr-1">
              <button
                type="button"
                onClick={() => handleCopy(row.value)}
                className="touch-target inline-flex items-center justify-center cursor-pointer rounded p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                aria-label={t("common.copy")}
              >
                <Copy className="h-3.5 w-3.5" />
              </button>
              <button
                type="button"
                onClick={() => toggleVisible(row.id)}
                className="touch-target inline-flex items-center justify-center cursor-pointer rounded p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                aria-label={visibleValues[row.id] ? t("service.hide") : t("service.show")}
              >
                {visibleValues[row.id] ? (
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
            aria-label={`${t("service.hint")} ${index + 1}`}
            onChange={(e) => updateRow(row.id, "hint", e.target.value)}
            placeholder={t("service.hintPlaceholder")}
          />
          <button
            type="button"
            onClick={() => removeRow(row.id)}
            className="touch-target inline-flex items-center justify-center cursor-pointer justify-self-end rounded p-2 text-muted-foreground transition-colors hover:bg-danger-subtle hover:text-danger"
            aria-label={t("service.removeKey")}
          >
            <Trash2 className="h-4 w-4" />
          </button>
        </div>
      ))}
      <button
        id="service-add-key"
        type="button"
        onClick={addRow}
        aria-describedby={error?.code === "empty" ? "service-credentials-error" : undefined}
        className="inline-flex cursor-pointer items-center gap-1.5 rounded-lg border border-dashed border-border px-3 py-2 text-sm text-muted-foreground transition-colors hover:border-primary hover:bg-primary-subtle hover:text-link"
      >
        <Plus className="h-4 w-4" />
        {t("service.addKey")}
      </button>
    </fieldset>
  );
}
