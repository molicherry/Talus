import { AlertTriangle, Loader2 } from "lucide-react";
import { useTranslation } from "../../i18n";
import { Button } from "./button";

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: string;
  confirmLabel?: string;
  isPending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel,
  isPending = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const { t } = useTranslation();
  const resolvedConfirmLabel = confirmLabel ?? t("common.confirm");

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <button
        type="button"
        className="fixed inset-0 bg-black/60 backdrop-blur-sm"
        onClick={onCancel}
        aria-label={t("common.closeDialog")}
      />
      <div className="relative z-10 w-full max-w-md rounded-2xl border border-border bg-card p-6 shadow-elevated">
        <div className="flex items-start gap-4">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-danger-subtle">
            <AlertTriangle className="h-5 w-5 text-danger" />
          </div>
          <div className="flex-1">
            <h3 className="text-lg font-semibold text-foreground">{title}</h3>
            <p className="mt-2 text-sm text-muted-foreground">{message}</p>
          </div>
        </div>
        <div className="mt-6 flex justify-end gap-3">
          <Button type="button" variant="secondary" onClick={onCancel} disabled={isPending}>
            {t("common.cancel")}
          </Button>
          <Button type="button" variant="destructive" onClick={onConfirm} disabled={isPending}>
            {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            {isPending ? t("common.deleting") : resolvedConfirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
