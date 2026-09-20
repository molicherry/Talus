/**
 * Tracks module/preload load failures reported by Vite.
 *
 * `main.tsx` marks the error object when the browser fires `vite:preloadError`
 * (e.g. a long-lived tab requests a chunk deleted by a redeploy). The boundary
 * checks identity against the error it actually caught, instead of a
 * "something failed in the last N seconds" flag — that flag could misclassify
 * an unrelated render error that happened to follow a chunk failure.
 */

const marked = new WeakSet<object>();

function asObject(value: unknown): object | null {
  return value !== null && (typeof value === "object" || typeof value === "function")
    ? (value as object)
    : null;
}

/** Remember the exact error a chunk-load failure produced (identity-based). */
export function markChunkError(error?: unknown): void {
  const target = asObject(error);
  if (target) marked.add(target);
}

/**
 * Whether `error` is a chunk-load failure. Prefers identity (set by the
 * `vite:preloadError` listener); falls back to the module-load message, which
 * browsers/bundlers word differently but reliably mention a dynamic import.
 */
export function isChunkError(error: unknown): boolean {
  const target = asObject(error);
  if (target && marked.has(target)) return true;
  return /imported module|ChunkLoadError|loading chunk|dynamically imported/i.test(
    (error as Error | null | undefined)?.message ?? "",
  );
}
