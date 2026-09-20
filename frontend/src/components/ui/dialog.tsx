import { X } from "lucide-react";
import { type ReactNode, useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";

import { useTranslation } from "../../i18n";
import { cn } from "../../lib/utils";

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  className?: string;
  /**
   * When false, Esc and backdrop clicks no longer close the dialog. Callers
   * set this while a request is in flight so every dismiss affordance obeys
   * the same rule (FE06 relies on this for the shared modal contract).
   */
  dismissible?: boolean;
}

/**
 * Modal built on the native `<dialog>` element.
 *
 * `showModal()` lifts the element into the top layer but does NOT reparent it
 * in the DOM, so the content is portaled to `document.body`: a `<form>` placed
 * inside an outer `<form>` would otherwise be invalid HTML (the browser drops
 * the inner form). React events still bubble along the React tree through the
 * portal, so form owners must stop `submit` propagation themselves.
 */
export function Dialog({
  open,
  onClose,
  title,
  children,
  className,
  dismissible = true,
}: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const titleId = useId();
  const { t } = useTranslation();

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;

    const previouslyFocused = document.activeElement as HTMLElement | null;
    const previousOverflow = document.body.style.overflow;

    if (!dialog.open) dialog.showModal();
    document.body.style.overflow = "hidden";

    return () => {
      document.body.style.overflow = previousOverflow;
      if (dialog.open) dialog.close();
      previouslyFocused?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      className={cn(
        "m-auto max-h-[85vh] w-[calc(100vw-2rem)] max-w-lg overflow-y-auto rounded-2xl border border-border bg-card p-0 text-foreground shadow-elevated backdrop:bg-black/60",
        className,
      )}
      onCancel={(event) => {
        // Always take over closing so `open` stays the single source of truth.
        event.preventDefault();
        if (dismissible) onClose();
      }}
      onClick={(event) => {
        // A click landing on the dialog element itself is the backdrop.
        if (dismissible && event.target === dialogRef.current) onClose();
      }}
    >
      <div className="p-6">
        <div className="mb-4 flex items-start justify-between gap-4">
          <h2 id={titleId} className="text-lg font-semibold text-foreground">
            {title}
          </h2>
          <button
            type="button"
            onClick={dismissible ? onClose : undefined}
            disabled={!dismissible}
            aria-label={t("common.closeDialog")}
            className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        {children}
      </div>
    </dialog>,
    document.body,
  );
}
