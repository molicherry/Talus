import { AlertTriangle, Loader2 } from "lucide-react";
import { useRef } from "react";
import { useTranslation } from "../../i18n";
import { Button } from "./button";
import { Dialog } from "./dialog";

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
  const cancelRef = useRef<HTMLButtonElement>(null);

  if (!open) return null;

  return (
    <Dialog
      open={open}
      title={title}
      onClose={onCancel}
      dismissible={!isPending}
      initialFocusRef={cancelRef}
      className="max-w-md"
    >
      <div className="flex items-start gap-4">
        <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-danger-subtle">
          <AlertTriangle className="h-5 w-5 text-danger" />
        </div>
        <div className="flex-1">
          <p className="text-sm text-muted-foreground">{message}</p>
        </div>
      </div>
      <div className="mt-6 flex justify-end gap-3">
        <Button
          ref={cancelRef}
          type="button"
          variant="secondary"
          onClick={onCancel}
          disabled={isPending}
        >
          {t("common.cancel")}
        </Button>
        <Button
          type="button"
          variant="destructive"
          onClick={() => {
            if (!isPending) onConfirm();
          }}
          disabled={isPending}
        >
          {isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          {isPending ? t("common.deleting") : resolvedConfirmLabel}
        </Button>
      </div>
    </Dialog>
  );
}
