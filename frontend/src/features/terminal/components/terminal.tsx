import {
  ArrowLeft,
  ArrowRight,
  ClipboardCopy,
  ClipboardPaste,
  Eraser,
  Loader2,
  Maximize2,
  Minimize2,
  Plug,
  Search,
  Unplug,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "../../../i18n";
import { Link } from "react-router-dom";
import { describeConnection } from "../lib/connection-status";
import { TERMINAL_SHORTCUTS } from "../lib/terminal-input";
import { useTerminal } from "../hooks/use-terminal";

const MAX_RETRIES = 5;

interface TerminalViewProps {
  serverId: number;
  serverName: string;
  host: string;
  port: number;
}

/**
 * A toolbar control. `touch-target` guarantees the 44px touch target on coarse
 * pointers (a phone in landscape included) while the density classes keep the
 * desktop toolbar compact.
 */
const controlClass =
  "touch-target inline-flex h-11 min-w-11 items-center justify-center gap-1.5 rounded-lg px-2.5 text-xs font-medium " +
  "transition-colors disabled:cursor-not-allowed disabled:opacity-40 sm:h-8 sm:min-w-8 sm:px-2 " +
  "bg-gray-100 text-gray-700 hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-300 dark:hover:bg-gray-700";

export function TerminalView({ serverId, serverName, host, port }: TerminalViewProps) {
  const {
    terminalRef,
    status,
    error,
    retryCount,
    generation,
    retryPending,
    fontSize,
    connect,
    disconnect,
    cancelRetry,
    increaseFontSize,
    decreaseFontSize,
    resetFontSize,
    search,
    clearSearch,
    clearTerminal,
    copySelection,
    readClipboard,
    pasteText,
    focusTerminal,
    setShortcuts,
  } = useTerminal(serverId);
  const { t } = useTranslation();
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [notice, setNotice] = useState<string | null>(null);
  const [pendingPaste, setPendingPaste] = useState<{
    text: string;
    lines: number;
    preview: string;
    generation: number;
  } | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    connect();
    return () => {
      disconnect();
    };
  }, [connect, disconnect]);

  // Fullscreen is a browser capability on the terminal container: it must work in
  // both directions (Esc exits it too) and the terminal refits on the way in and out.
  useEffect(() => {
    const onFullscreenChange = () => {
      setIsFullscreen(document.fullscreenElement === containerRef.current);
    };
    document.addEventListener("fullscreenchange", onFullscreenChange);
    return () => document.removeEventListener("fullscreenchange", onFullscreenChange);
  }, []);

  const toggleFullscreen = useCallback(() => {
    const element = containerRef.current ?? document.documentElement;
    if (document.fullscreenElement === element) {
      void document.exitFullscreen();
      return;
    }
    if (typeof element.requestFullscreen !== "function") {
      setNotice(t("terminal.fullscreenUnsupported"));
      return;
    }
    void element.requestFullscreen().catch(() => {
      setNotice(t("terminal.fullscreenUnsupported"));
    });
  }, [t]);

  // The notice is a transient explanation, never a state the user has to clear.
  useEffect(() => {
    if (notice === null) return;
    const timer = setTimeout(() => setNotice(null), 4000);
    return () => clearTimeout(timer);
  }, [notice]);

  const view = describeConnection({ status, retryCount, maxRetries: MAX_RETRIES, error, retryPending });

  const runCopy = useCallback(async () => {
    // The write is awaited: reporting "copied" before the browser accepted it would
    // claim success for a denied permission (F04).
    const outcome = await copySelection();
    if (outcome.kind === "copy-empty") setNotice(t("terminal.copyEmpty"));
    else if (outcome.kind === "copy-denied") setNotice(t("terminal.clipboardDenied"));
    else setNotice(t("terminal.copied", { bytes: outcome.bytes }));
  }, [copySelection, t]);

  const runPaste = useCallback(async () => {
    const outcome = await readClipboard();
    switch (outcome.kind) {
      case "paste-empty":
        setNotice(t("terminal.pasteEmpty"));
        return;
      case "paste-denied":
        setNotice(t("terminal.clipboardDenied"));
        return;
      case "paste-multiline":
        // Multi-line payloads are confirmed first: pasting a script by accident must
        // not be one keystroke away. The generation travels with the payload so a
        // reconnect in between invalidates the confirmation (F03).
        setPendingPaste({
          text: outcome.text,
          lines: outcome.lines,
          preview: outcome.preview,
          generation: outcome.generation,
        });
        return;
      case "paste-stale":
        // The read finished after the session was replaced; nothing is typed.
        setNotice(t("terminal.pasteStale"));
        return;
      default:
        pasteText(outcome.text, outcome.generation);
        setNotice(t("terminal.pasted", { lines: outcome.lines }));
        focusTerminal();
    }
  }, [readClipboard, pasteText, focusTerminal, t]);

  const confirmPaste = useCallback(() => {
    if (pendingPaste) {
      // pasteText drops the payload when the session it was read in is gone (F03).
      pasteText(pendingPaste.text, pendingPaste.generation);
      setNotice(t("terminal.pasted", { lines: pendingPaste.lines }));
      focusTerminal();
    }
    setPendingPaste(null);
  }, [pendingPaste, pasteText, focusTerminal, t]);

  const runSearch = useCallback(
    (direction: "next" | "previous") => {
      if (searchQuery.length === 0) return;
      const found = search(searchQuery, direction);
      if (!found) setNotice(t("terminal.searchNoMatch", { query: searchQuery }));
    },
    [search, searchQuery, t],
  );

  const closeSearch = useCallback(() => {
    setSearchOpen(false);
    clearSearch();
  }, [clearSearch]);

  // Pending clipboard work belongs to one session: when the connection generation
  // changes (disconnect, reconnect, another host) it is dropped instead of being
  // offered for a shell it was not read from (F03).
  useEffect(() => {
    setPendingPaste(null);
    setNotice(null);
  }, [generation]);

  // The terminal-local shortcuts. They are installed on the xterm instance, which
  // handles them *before* encoding the key, so Ctrl+F opens the local search instead
  // of sending 0x06, and paste combinations run the confirmation flow (F01/F02).
  // Nothing is bound at document level: a local editable control - the search box -
  // keeps its native editing behaviour, so pasting there can never reach the shell.
  useEffect(() => {
    setShortcuts({
      onSearch: () => setSearchOpen(true),
      onCopy: () => void runCopy(),
      onPaste: () => void runPaste(),
      onClear: clearTerminal,
    });
    return () => setShortcuts(null);
  }, [setShortcuts, runCopy, runPaste, clearTerminal]);

  const statusIcon = () => {
    if (view.tone === "ok") return <Wifi className="h-3.5 w-3.5 text-green-400" aria-hidden />;
    if (view.tone === "error") return <WifiOff className="h-3.5 w-3.5 text-danger" aria-hidden />;
    if (view.tone === "busy") return <Loader2 className="h-3.5 w-3.5 animate-spin text-yellow-500" aria-hidden />;
    return <WifiOff className="h-3.5 w-3.5 text-gray-400 dark:text-gray-500" aria-hidden />;
  };

  return (
    <div ref={containerRef} className="flex h-full flex-col overflow-hidden bg-background">
      {/* Toolbar: server identity, connection state and every control (REQ-09). */}
      <div className="flex flex-shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-gray-200 px-2 py-1.5 dark:border-gray-800">
        <Link
          to={`/servers/${serverId}`}
          aria-label={t("terminal.backToServer")}
          className="inline-flex h-11 min-w-11 items-center gap-1.5 rounded-lg px-2 text-sm text-gray-500 transition-colors hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200 sm:h-8 sm:min-w-8"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden />
        </Link>
        <div className="flex min-w-0 flex-col leading-tight">
          <span className="truncate text-sm font-medium text-foreground" data-testid="terminal-server-name">
            {serverName}
          </span>
          <span className="truncate font-mono text-[11px] text-muted-foreground" data-testid="terminal-server-address">
            {host}:{port}
          </span>
        </div>

        <span
          role="status"
          aria-live="polite"
          data-connection-status={status}
          data-tone={view.tone}
          className="inline-flex items-center gap-1.5 rounded-full border border-border px-2 py-0.5 text-xs"
        >
          {statusIcon()}
          <span>
            {t(view.labelKey, view.vars)}
            {view.isNewSession && t("terminal.newSessionSuffix")}
          </span>
        </span>

        <div className="ml-auto flex flex-wrap items-center gap-1">
          {/* Primary control: connect / reconnect / cancel / disconnect. */}
          {view.canCancelRetry && (
            <button
              type="button"
              className={controlClass}
              onClick={status === "connecting" ? disconnect : cancelRetry}
              aria-label={t("terminal.cancelConnect")}
            >
              <X className="h-3.5 w-3.5" aria-hidden />
              <span className="hidden sm:inline">{t("terminal.cancelConnect")}</span>
            </button>
          )}
          {view.canDisconnect && (
            <button
              type="button"
              className={controlClass}
              onClick={disconnect}
              aria-label={t("terminal.disconnect")}
            >
              <Unplug className="h-3.5 w-3.5" aria-hidden />
              <span className="hidden sm:inline">{t("terminal.disconnect")}</span>
            </button>
          )}
          {view.canConnect && (
            <button
              type="button"
              className={controlClass}
              onClick={() => {
                connect();
                focusTerminal();
              }}
              aria-label={view.isRetry ? t("terminal.reconnect") : t("terminal.connect")}
            >
              <Plug className="h-3.5 w-3.5" aria-hidden />
              <span className="hidden sm:inline">
                {view.isRetry ? t("terminal.reconnect") : t("terminal.connect")}
              </span>
            </button>
          )}

          {/* Desktop-only controls; narrow layouts get them behind "More". */}
          <div className="hidden flex-wrap items-center gap-1 sm:flex">
            <button
              type="button"
              className={controlClass}
              aria-label={t("terminal.search")}
              aria-pressed={searchOpen}
              onClick={() => (searchOpen ? closeSearch() : setSearchOpen(true))}
            >
              <Search className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.fontDecrease")} onClick={decreaseFontSize}>
              A−
            </button>
            <span data-font-size={fontSize} className="px-1 text-[11px] text-muted-foreground">
              {fontSize}px
            </span>
            <button type="button" className={controlClass} aria-label={t("terminal.fontIncrease")} onClick={increaseFontSize}>
              A+
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.fontReset")} onClick={resetFontSize}>
              {t("terminal.fontResetShort")}
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.copy")} onClick={() => void runCopy()}>
              <ClipboardCopy className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.paste")} onClick={() => void runPaste()}>
              <ClipboardPaste className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.clear")} onClick={clearTerminal}>
              <Eraser className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button
              type="button"
              className={controlClass}
              aria-label={isFullscreen ? t("terminal.exitFullscreen") : t("terminal.fullscreen")}
              aria-pressed={isFullscreen}
              data-fullscreen={isFullscreen}
              onClick={toggleFullscreen}
            >
              {isFullscreen ? <Minimize2 className="h-3.5 w-3.5" aria-hidden /> : <Maximize2 className="h-3.5 w-3.5" aria-hidden />}
            </button>
          </div>

          {/* Narrow layouts: one menu holds the same actions, 44px tall each. */}
          <details className="sm:hidden" data-testid="terminal-more">
            <summary className={`${controlClass} cursor-pointer list-none`} aria-label={t("terminal.more")}>
              {t("terminal.more")}
            </summary>
            <div className="absolute right-2 z-20 mt-1 flex w-44 flex-col rounded-xl border border-border bg-card p-1 shadow-lg">
              <MenuButton label={t("terminal.search")} onClick={() => setSearchOpen(true)}>
                <Search className="h-4 w-4" aria-hidden />
              </MenuButton>
              <MenuButton label={t("terminal.fontDecrease")} onClick={decreaseFontSize}>A−</MenuButton>
              <MenuButton label={t("terminal.fontIncrease")} onClick={increaseFontSize}>A+</MenuButton>
              <MenuButton label={t("terminal.fontReset")} onClick={resetFontSize}>{t("terminal.fontResetShort")}</MenuButton>
              <MenuButton label={t("terminal.copy")} onClick={() => void runCopy()}>
                <ClipboardCopy className="h-4 w-4" aria-hidden />
              </MenuButton>
              <MenuButton label={t("terminal.paste")} onClick={() => void runPaste()}>
                <ClipboardPaste className="h-4 w-4" aria-hidden />
              </MenuButton>
              <MenuButton label={t("terminal.clear")} onClick={clearTerminal}>
                <Eraser className="h-4 w-4" aria-hidden />
              </MenuButton>
              <MenuButton label={t("terminal.fullscreen")} onClick={toggleFullscreen}>
                <Maximize2 className="h-4 w-4" aria-hidden />
              </MenuButton>
            </div>
          </details>
        </div>
      </div>

      {/* Search bar: local scrollback only, Esc closes it. */}
      {searchOpen && (
        <div className="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-gray-200 px-3 py-2 dark:border-gray-800">
          <input
            autoFocus
            type="text"
            value={searchQuery}
            aria-label={t("terminal.search")}
            placeholder={t("terminal.searchPlaceholder")}
            onChange={(event) => setSearchQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                runSearch(event.shiftKey ? "previous" : "next");
              } else if (event.key === "Escape") {
                // Escape belongs to the focused control: with the search box open it
                // closes the search, and it never reaches the shell (F01).
                event.preventDefault();
                closeSearch();
              }
            }}
            // min-w-0 lets the field shrink on a 375px screen instead of pushing the
            // search actions out of the viewport (F09).
            className="touch-target h-11 min-w-0 flex-1 basis-40 rounded-lg border border-border bg-background px-3 text-sm text-foreground sm:h-8"
          />
          <button type="button" className={controlClass} aria-label={t("terminal.searchPrevious")} onClick={() => runSearch("previous")}>
            <ArrowLeft className="h-3.5 w-3.5" aria-hidden />
          </button>
          <button type="button" className={controlClass} aria-label={t("terminal.searchNext")} onClick={() => runSearch("next")}>
            <ArrowRight className="h-3.5 w-3.5" aria-hidden />
          </button>
          <button type="button" className={controlClass} aria-label={t("terminal.searchClose")} onClick={closeSearch}>
            <X className="h-3.5 w-3.5" aria-hidden />
          </button>
        </div>
      )}

      {/* Connection failures are readable outside the terminal. */}
      {error && (
        <div className="flex-shrink-0 border-b border-danger/30 bg-danger-subtle px-4 py-2" role="alert">
          <p className="text-sm text-danger">{error}</p>
        </div>
      )}

      {notice && (
        <div className="flex-shrink-0 border-b border-border bg-muted px-4 py-2" role="status">
          <p className="text-xs text-muted-foreground">{notice}</p>
        </div>
      )}

      {pendingPaste && (
        <div className="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-yellow-300 bg-yellow-50 px-4 py-2 dark:border-yellow-800 dark:bg-yellow-900/20" role="alertdialog" aria-label={t("terminal.pasteConfirmTitle")}>
          <p className="text-xs text-yellow-800 dark:text-yellow-200">
            {t("terminal.pasteConfirm", { lines: pendingPaste.lines, preview: pendingPaste.preview })}
          </p>
          <button type="button" className={controlClass} onClick={confirmPaste}>
            {t("terminal.pasteConfirmSend")}
          </button>
          <button type="button" className={controlClass} onClick={() => setPendingPaste(null)}>
            {t("terminal.pasteConfirmCancel")}
          </button>
        </div>
      )}

      {/* The terminal owns the remaining height; the container is the fit target. */}
      <div className="min-h-0 flex-1 overflow-hidden p-1">
        <div
          ref={terminalRef}
          data-testid="terminal-surface"
          className="h-full w-full"
          onClick={() => focusTerminal()}
        />
      </div>

      {/* The documented shortcut surface. */}
      <div className="hidden flex-shrink-0 flex-wrap gap-x-3 gap-y-1 border-t border-gray-200 px-3 py-1 text-[11px] text-muted-foreground sm:flex dark:border-gray-800">
        {TERMINAL_SHORTCUTS.map((hint) => (
          <span key={hint.keys}>
            <kbd className="font-mono">{hint.keys}</kbd> {t(hint.labelKey)}
          </span>
        ))}
      </div>
    </div>
  );
}

function MenuButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      className="touch-target inline-flex h-11 items-center gap-2 rounded-lg px-3 text-left text-xs font-medium text-foreground hover:bg-muted"
    >
      {children}
      <span>{label}</span>
    </button>
  );
}
