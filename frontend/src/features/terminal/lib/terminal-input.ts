/**
 * Input classification and the documented shortcut surface (REQ-09 "剪贴板与清屏").
 *
 * Pasting is done by the user through an explicit action, so a multi-line payload is
 * described before it reaches the shell: the UI shows how many lines are about to be
 * sent instead of silently executing them. Nothing here runs on the remote host.
 */

/** How a clipboard payload should be presented before it is sent. */
export interface PasteShape {
  kind: "empty" | "single-line" | "multi-line";
  /** Number of lines that would be sent, ignoring a single trailing newline. */
  lines: number;
  /** Payload size in bytes (UTF-8), so a large paste can be described. */
  bytes: number;
  /** First line, trimmed and capped, for the confirmation prompt. */
  preview: string;
}

const PREVIEW_LIMIT = 60;

/** Classifies clipboard text so a multi-line paste is confirmed, never silent. */
export function classifyPaste(text: string): PasteShape {
  // Normalize CRLF/CR so a Windows clipboard is not counted as one huge line.
  const normalized = text.replace(/\r\n?/g, "\n");
  if (normalized.length === 0) {
    return { kind: "empty", lines: 0, bytes: 0, preview: "" };
  }
  const body = normalized.endsWith("\n") ? normalized.slice(0, -1) : normalized;
  const lines = body.split("\n").length;
  const firstLine = body.split("\n")[0] ?? "";
  const preview = firstLine.length > PREVIEW_LIMIT ? `${firstLine.slice(0, PREVIEW_LIMIT)}…` : firstLine;
  return {
    kind: lines > 1 ? "multi-line" : "single-line",
    lines,
    bytes: new TextEncoder().encode(normalized).length,
    preview,
  };
}

/** One documented shortcut: the keys and the translation key describing them. */
export interface ShortcutHint {
  keys: string;
  labelKey: string;
}

/**
 * The keyboard surface the toolbar advertises. The copy/paste combinations follow
 * the terminal convention (Ctrl+Shift+C/V) because plain Ctrl+C must reach the
 * remote shell as an interrupt.
 */
export const TERMINAL_SHORTCUTS: readonly ShortcutHint[] = [
  { keys: "Ctrl+Shift+C", labelKey: "terminal.shortcutCopy" },
  { keys: "Ctrl+Shift+V", labelKey: "terminal.shortcutPaste" },
  { keys: "Ctrl+F", labelKey: "terminal.shortcutSearch" },
  { keys: "Esc", labelKey: "terminal.shortcutCloseSearch" },
  // Ctrl+L is deliberately not used: readline owns it as the remote clear-screen.
  { keys: "Ctrl+Shift+L", labelKey: "terminal.shortcutClear" },
];
