/**
 * Terminal display preferences (REQ-09 "字体与主题").
 *
 * The font size is a client-only preference: it is clamped to a usable range and
 * remembered locally, so reopening the terminal keeps the user's choice. Storage is
 * injected so the rules are testable without a browser and so a corrupt or blocked
 * localStorage can never break the terminal.
 */

export const TERMINAL_FONT_SIZE_DEFAULT = 14;
export const TERMINAL_FONT_SIZE_MIN = 10;
export const TERMINAL_FONT_SIZE_MAX = 24;

/** The storage key holding the remembered font size. */
export const TERMINAL_FONT_SIZE_KEY = "talus-terminal-font-size";

/** The subset of Storage this module needs. */
export interface PreferenceStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/** Clamps a font size into the supported range, falling back on the default. */
export function clampFontSize(value: number): number {
  if (!Number.isFinite(value)) return TERMINAL_FONT_SIZE_DEFAULT;
  const rounded = Math.round(value);
  if (rounded < TERMINAL_FONT_SIZE_MIN) return TERMINAL_FONT_SIZE_MIN;
  if (rounded > TERMINAL_FONT_SIZE_MAX) return TERMINAL_FONT_SIZE_MAX;
  return rounded;
}

/** Steps the font size by delta and clamps the result. */
export function stepFontSize(current: number, delta: number): number {
  return clampFontSize(clampFontSize(current) + delta);
}

/** The browser's localStorage, or null when it is unavailable (SSR, privacy mode). */
function browserStore(): PreferenceStore | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** Reads the remembered font size; anything unusable yields the default. */
export function loadFontSize(store: PreferenceStore | null = browserStore()): number {
  if (!store) return TERMINAL_FONT_SIZE_DEFAULT;
  try {
    const raw = store.getItem(TERMINAL_FONT_SIZE_KEY);
    // An absent, empty or whitespace-only value is not a remembered choice:
    // Number("") is 0, which would be clamped to the minimum instead.
    if (raw === null || raw.trim() === "") return TERMINAL_FONT_SIZE_DEFAULT;
    const parsed = Number(raw);
    // A stored value outside the range is clamped rather than rejected: the range
    // may have narrowed since it was written.
    return Number.isFinite(parsed) ? clampFontSize(parsed) : TERMINAL_FONT_SIZE_DEFAULT;
  } catch {
    return TERMINAL_FONT_SIZE_DEFAULT;
  }
}

/** Remembers the font size. Storage failures are ignored: it is a preference. */
export function saveFontSize(size: number, store: PreferenceStore | null = browserStore()): number {
  const clamped = clampFontSize(size);
  if (!store) return clamped;
  try {
    store.setItem(TERMINAL_FONT_SIZE_KEY, String(clamped));
  } catch {
    // Quota or privacy restrictions must not break the terminal.
  }
  return clamped;
}
