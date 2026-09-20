/**
 * Tracks module/preload load failures reported by Vite.
 *
 * `main.tsx` marks an error when the browser fires `vite:preloadError` (e.g. a
 * long-lived tab requests a chunk deleted by a redeploy). The error boundary
 * consults this instead of guessing from the error message, which differs
 * between browsers and bundlers.
 */
let lastChunkErrorAt = 0;

export function markChunkError(): void {
  lastChunkErrorAt = Date.now();
}

/** True when a chunk failure was reported within the last few seconds. */
export function isRecentChunkError(withinMs = 5000): boolean {
  return lastChunkErrorAt > 0 && Date.now() - lastChunkErrorAt < withinMs;
}
