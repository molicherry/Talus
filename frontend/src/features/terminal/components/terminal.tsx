import {
  ArrowLeft,
  ArrowRight,
  ClipboardCopy,
  ClipboardPaste,
  Eraser,
  Ellipsis,
  Loader2,
  FolderOpen,
  Maximize2,
  Minimize2,
  Plug,
  Search,
  SquareTerminal,
  Unplug,
  Wifi,
  WifiOff,
  X,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "../../../i18n";
import { Link } from "react-router-dom";
import { describeConnection } from "../lib/connection-status";
import { classifyPaste, TERMINAL_SHORTCUTS } from "../lib/terminal-input";
import { useTerminal } from "../hooks/use-terminal";
import "@xterm/xterm/css/xterm.css";
import "./terminal.css";

const MAX_RETRIES = 5;

interface TerminalViewProps {
  serverId: number;
  serverName: string;
  host: string;
  port: number;
  filesOpen?: boolean;
  onToggleFiles?: () => void;
}

/**
 * A toolbar control. `touch-target` guarantees the 44px touch target on coarse
 * pointers (a phone in landscape included) while the density classes keep the
 * desktop toolbar compact.
 */
const controlClass = "ssh-control touch-target";

export function TerminalView({ serverId, serverName, host, port, filesOpen = false, onToggleFiles }: TerminalViewProps) {
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
  // The generation is read through a ref inside the shortcut callbacks, which are
  // installed once per terminal.
  const generationRefForPaste = useRef(generation);
  generationRefForPaste.current = generation;

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

  /** Applies a payload that is already in hand (a native event or a confirmed read). */
  const applyPaste = useCallback(
    (text: string, generation?: number) => {
      const shape = classifyPaste(text);
      if (shape.kind === "empty") {
        setNotice(t("terminal.pasteEmpty"));
        return;
      }
      if (shape.kind === "multi-line") {
        // Multi-line payloads are confirmed first, and the confirmation is bound to
        // the connection generation it was read in (F03).
        setPendingPaste({
          text,
          lines: shape.lines,
          preview: shape.preview,
          generation: generation ?? -1,
        });
        return;
      }
      pasteText(text, generation);
      setNotice(t("terminal.pasted", { lines: shape.lines }));
      focusTerminal();
    },
    [pasteText, focusTerminal, t],
  );

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
      // pasteText drops the payload when the session it was read in is gone (F03);
      // a payload from a native event carries the generation captured at delivery.
      pasteText(pendingPaste.text, pendingPaste.generation >= 0 ? pendingPaste.generation : undefined);
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
      // A native event's payload skips the clipboard API entirely (R02); the
      // generation is captured here so a reconnect still invalidates the payload.
      onPastePayload: (text: string) => applyPaste(text, generationRefForPaste.current),
      onClear: clearTerminal,
    });
    return () => setShortcuts(null);
  }, [setShortcuts, runCopy, runPaste, applyPaste, clearTerminal]);

  const statusIcon = () => {
    if (view.tone === "ok") return <Wifi className="h-3.5 w-3.5" aria-hidden />;
    if (view.tone === "busy") return <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />;
    return <WifiOff className="h-3.5 w-3.5" aria-hidden />;
  };

  return (
    <div ref={containerRef} className="ssh-workspace">
      {/* Toolbar: server identity, connection state and every control (REQ-09). */}
      <div className="ssh-toolbar">
        <div className="ssh-identity">
          <Link
            to={`/servers/${serverId}`}
            aria-label={t("terminal.backToServer")}
            title={t("terminal.backToServer")}
            className={`${controlClass} ssh-back`}
          >
            <ArrowLeft className="h-4 w-4" aria-hidden />
          </Link>
          <span className="ssh-terminal-icon" aria-hidden="true">
            <SquareTerminal className="h-4 w-4" />
          </span>
          <div className="ssh-host">
            <span className="ssh-host-name" data-testid="terminal-server-name" title={serverName}>
              {serverName}
            </span>
            <span className="ssh-host-address" data-testid="terminal-server-address" title={`${host}:${port}`}>
              {host}:{port}
            </span>
          </div>

          <span
            role="status"
            aria-live="polite"
            data-connection-status={status}
            data-tone={view.tone}
            className="ssh-connection-status"
          >
            {statusIcon()}
            <span>
              {t(view.labelKey, view.vars)}
              {view.isNewSession && t("terminal.newSessionSuffix")}
            </span>
          </span>
        </div>

        <div className="ssh-actions">
          {/* Primary control: connect / reconnect / cancel / disconnect. */}
          {view.canCancelRetry && (
            <button
              type="button"
              className={`${controlClass} ssh-connection-action`}
              onClick={status === "connecting" ? disconnect : cancelRetry}
              aria-label={t("terminal.cancelConnect")}
            >
              <X className="h-3.5 w-3.5" aria-hidden />
              <span>{t("terminal.cancelConnect")}</span>
            </button>
          )}
          {view.canDisconnect && (
            <button
              type="button"
              className={`${controlClass} ssh-disconnect-action`}
              onClick={disconnect}
              aria-label={t("terminal.disconnect")}
            >
              <Unplug className="h-3.5 w-3.5" aria-hidden />
              <span>{t("terminal.disconnect")}</span>
            </button>
          )}
          {view.canConnect && (
            <button
              type="button"
              className={`${controlClass} ssh-connection-action`}
              onClick={() => {
                connect();
                focusTerminal();
              }}
              aria-label={view.isRetry ? t("terminal.reconnect") : t("terminal.connect")}
            >
              <Plug className="h-3.5 w-3.5" aria-hidden />
              <span>
                {view.isRetry ? t("terminal.reconnect") : t("terminal.connect")}
              </span>
            </button>
          )}

          {onToggleFiles && <button type="button" className={controlClass} aria-label={t("files.title")} aria-pressed={filesOpen} onClick={onToggleFiles}>
            <FolderOpen className="h-3.5 w-3.5" aria-hidden /><span>{t("files.title")}</span>
          </button>}

          {/* Desktop-only controls; narrow layouts get them behind "More". */}
          <div className="ssh-desktop-actions hidden sm:flex">
            <button
              type="button"
              className={controlClass}
              aria-label={t("terminal.search")}
              title={t("terminal.search")}
              aria-pressed={searchOpen}
              onClick={() => (searchOpen ? closeSearch() : setSearchOpen(true))}
            >
              <Search className="h-3.5 w-3.5" aria-hidden />
            </button>
            <span className="ssh-action-divider" aria-hidden="true" />
            <div className="ssh-font-controls">
              <button type="button" className={controlClass} aria-label={t("terminal.fontDecrease")} title={t("terminal.fontDecrease")} onClick={decreaseFontSize}>
                A−
              </button>
              <span data-font-size={fontSize} className="ssh-font-size">
                {fontSize}px
              </span>
              <button type="button" className={controlClass} aria-label={t("terminal.fontIncrease")} title={t("terminal.fontIncrease")} onClick={increaseFontSize}>
                A+
              </button>
              <button type="button" className={controlClass} aria-label={t("terminal.fontReset")} title={t("terminal.fontReset")} onClick={resetFontSize}>
                {t("terminal.fontResetShort")}
              </button>
            </div>
            <span className="ssh-action-divider" aria-hidden="true" />
            <button type="button" className={controlClass} aria-label={t("terminal.copy")} title={t("terminal.copy")} onClick={() => void runCopy()}>
              <ClipboardCopy className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.paste")} title={t("terminal.paste")} onClick={() => void runPaste()}>
              <ClipboardPaste className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button type="button" className={controlClass} aria-label={t("terminal.clear")} title={t("terminal.clear")} onClick={clearTerminal}>
              <Eraser className="h-3.5 w-3.5" aria-hidden />
            </button>
            <button
              type="button"
              className={controlClass}
              aria-label={isFullscreen ? t("terminal.exitFullscreen") : t("terminal.fullscreen")}
              title={isFullscreen ? t("terminal.exitFullscreen") : t("terminal.fullscreen")}
              aria-pressed={isFullscreen}
              data-fullscreen={isFullscreen}
              onClick={toggleFullscreen}
            >
              {isFullscreen ? <Minimize2 className="h-3.5 w-3.5" aria-hidden /> : <Maximize2 className="h-3.5 w-3.5" aria-hidden />}
            </button>
          </div>

          {/* Narrow layouts: one menu holds the same actions, 44px tall each. */}
          <details className="ssh-more sm:hidden" data-testid="terminal-more">
            <summary className={`${controlClass} cursor-pointer list-none`} aria-label={t("terminal.more")}>
              <Ellipsis className="h-4 w-4" aria-hidden="true" />
              <span>{t("terminal.more")}</span>
            </summary>
            <div className="ssh-more-menu">
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
        <div className="ssh-search-panel">
          <Search className="h-4 w-4 shrink-0" aria-hidden="true" />
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
            className="ssh-search-input touch-target"
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
        <div className="ssh-message ssh-message--error" role="alert">
          <p>{error}</p>
        </div>
      )}

      {notice && (
        <div className="ssh-message" role="status">
          <p>{notice}</p>
        </div>
      )}

      {pendingPaste && (
        <div className="ssh-message ssh-message--confirm" role="alertdialog" aria-label={t("terminal.pasteConfirmTitle")}>
          <p>
            {t("terminal.pasteConfirm", { lines: pendingPaste.lines, preview: pendingPaste.preview })}
          </p>
          <button type="button" className={`${controlClass} ssh-connection-action`} onClick={confirmPaste}>
            {t("terminal.pasteConfirmSend")}
          </button>
          <button type="button" className={controlClass} onClick={() => setPendingPaste(null)}>
            {t("terminal.pasteConfirmCancel")}
          </button>
        </div>
      )}

      {/* The terminal owns the remaining height; the container is the fit target. */}
      <div className="ssh-terminal-stage">
        <div
          ref={terminalRef}
          data-testid="terminal-surface"
          className="h-full w-full"
          onClick={() => focusTerminal()}
        />
      </div>

      {/* The documented shortcut surface. */}
      <div className="ssh-shortcuts hidden sm:flex">
        {TERMINAL_SHORTCUTS.map((hint) => (
          <span key={hint.keys}>
            <kbd>{hint.keys}</kbd> {t(hint.labelKey)}
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
      className="ssh-menu-button touch-target"
    >
      {children}
      <span>{label}</span>
    </button>
  );
}
