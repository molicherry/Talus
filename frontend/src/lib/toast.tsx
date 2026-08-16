import { useEffect, useState } from "react";
import { createPortal } from "react-dom";

/**
 * Minimal toast system replacing `sonner`.
 *
 * The app only calls toast.success(msg) / toast.error(msg) with a string and
 * renders <Toaster position="top-right"> with a card-style override — that
 * whole surface is covered here. Auto-dismiss after 4s (sonner default),
 * top-right stack, same visual tokens as the old toastOptions.
 */

interface ToastItem {
  id: number;
  message: string;
  type: "success" | "error";
}

const AUTO_DISMISS_MS = 4000;

let toasts: ToastItem[] = [];
const listeners = new Set<() => void>();
let nextId = 1;

function emit(type: ToastItem["type"], message: string): void {
  const id = nextId++;
  toasts = [...toasts, { id, message, type }];
  listeners.forEach((l) => l());
  window.setTimeout(() => {
    toasts = toasts.filter((t) => t.id !== id);
    listeners.forEach((l) => l());
  }, AUTO_DISMISS_MS);
}

export const toast = {
  success: (message: string) => emit("success", message),
  error: (message: string) => emit("error", message),
};

export function Toaster() {
  const [, forceRender] = useState(0);

  useEffect(() => {
    const listener = () => forceRender((n) => n + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);

  if (toasts.length === 0) return null;

  return createPortal(
    <div className="pointer-events-none fixed right-4 top-4 z-[100] flex w-80 flex-col gap-2">
      {toasts.map((t) => (
        <div
          key={t.id}
          role="status"
          className="pointer-events-auto flex items-start gap-2.5 rounded-lg border px-4 py-3 text-sm shadow-lg"
          style={{
            background: "var(--color-card)",
            color: "var(--color-card-foreground)",
            borderColor: "var(--color-border)",
            boxShadow: "var(--shadow-elevated)",
          }}
        >
          <span
            aria-hidden
            className={
              t.type === "success"
                ? "mt-0.5 h-2 w-2 shrink-0 rounded-full bg-green-500"
                : "mt-0.5 h-2 w-2 shrink-0 rounded-full bg-red-500"
            }
          />
          <span className="break-words">{t.message}</span>
        </div>
      ))}
    </div>,
    document.body,
  );
}
