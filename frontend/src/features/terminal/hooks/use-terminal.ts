import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useCallback, useEffect, useRef, useState } from "react";
import type { TerminalServerMessage } from "../../../types/ssh";
import { classifyClose } from "../lib/connection-status";
import {
  TERMINAL_FONT_SIZE_DEFAULT,
  loadFontSize,
  saveFontSize,
  stepFontSize,
} from "../lib/terminal-preferences";
import { classifyPaste } from "../lib/terminal-input";
import { createTerminalSocket, sendTerminalMessage } from "../socket";

const MAX_RETRIES = 5;
const BASE_DELAY_MS = 2000;
const RESIZE_DEBOUNCE_MS = 300;
// How long a keyboard-initiated paste suppresses the native paste event for the same
// keystroke. Browsers fire it within the same task, so this is generous.
const PASTE_DEDUPE_MS = 400;

/** The outcome of a copy, so the toolbar can explain what happened. */
export type CopyOutcome =
  | { kind: "copied"; bytes: number }
  | { kind: "copy-empty" }
  | { kind: "copy-denied" };

/** The outcome of reading the clipboard, including the multi-line confirmation. */
export type PasteOutcome =
  | { kind: "paste-empty" }
  | { kind: "paste-denied" }
  | { kind: "paste-ready"; text: string; lines: number; generation: number }
  | { kind: "paste-multiline"; text: string; lines: number; preview: string; generation: number }
  // The read finished after the session it started in was replaced (F03).
  | { kind: "paste-stale" };

/** What the terminal does with a shortcut before xterm encodes it (F01). */
export interface ShortcutHandlers {
  onSearch: () => void;
  onCopy: () => void;
  /** Paste via the clipboard API (the toolbar button and keyboard shortcuts). */
  onPaste: () => void;
  /** Paste a payload a native event already delivered (no second clipboard read). */
  onPastePayload?: (text: string) => void;
  onClear: () => void;
}

export interface UseTerminalReturn {
  terminalRef: React.RefObject<HTMLDivElement | null>;
  status: "disconnected" | "connecting" | "connected" | "reconnecting";
  error: string | null;
  retryCount: number;
  /** The current connection generation; it changes on every connect/disconnect. */
  generation: number;
  /** True while an automatic retry is waiting for its timer to fire. */
  retryPending: boolean;
  fontSize: number;
  connect: () => void;
  disconnect: () => void;
  cancelRetry: () => void;
  increaseFontSize: () => void;
  decreaseFontSize: () => void;
  resetFontSize: () => void;
  search: (query: string, direction: "next" | "previous") => boolean;
  clearSearch: () => void;
  clearTerminal: () => void;
  copySelection: () => Promise<CopyOutcome>;
  readClipboard: () => Promise<PasteOutcome>;
  pasteText: (text: string, generation?: number) => void;
  focusTerminal: () => void;
  /** Installs the terminal-local shortcut handlers (F01/F02). */
  setShortcuts: (handlers: ShortcutHandlers | null) => void;
}

export function useTerminal(serverId: number): UseTerminalReturn {
  const terminalRef = useRef<HTMLDivElement | null>(null);
  const termInstanceRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const searchAddonRef = useRef<SearchAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const resizeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retryCountRef = useRef(0);
  const manualDisconnectRef = useRef(false);
  // handleResize is recreated per connect(); keep a ref so disconnect can
  // remove the exact listener that was added (otherwise reconnects leak
  // window "resize" listeners).
  const windowResizeHandlerRef = useRef<(() => void) | null>(null);
  // Layout changes that a window "resize" event never reports (sidebar toggles,
  // fullscreen, panel resizes) are observed on the container itself.
  const containerObserverRef = useRef<ResizeObserver | null>(null);
  // The font size is a remembered display preference (REQ-09 §字体与主题).
  const fontSizeRef = useRef(loadFontSize());
  // Every connect/disconnect starts a new connection generation. An async clipboard
  // read or a pending confirmation carries the generation it was started in, so a
  // late result can never be typed into a later shell (F03).
  const generationRef = useRef(0);
  // A size change made before the socket was ready must not be lost (F06).
  const pendingResizeRef = useRef(false);
  // Shortcut handlers are installed per terminal by the component (F01).
  const shortcutsRef = useRef<ShortcutHandlers | null>(null);
  // A paste keystroke is handled by the key handler; the native paste event the same
  // keystroke produces is ignored until this deadline, so one action sends one payload
  // (R01).
  const suppressNativePasteRef = useRef(0);
  // Set when the server refused the session, so the close is not retried (F07).
  const terminateRef = useRef(false);
  // The most specific failure text reported for this session: the server's own
  // message wins over the shorter close reason (F05/F07).
  const errorRef = useRef<string | null>(null);

  /** Records a failure, keeping the first (most specific) explanation. */
  const reportError = useCallback((text: string) => {
    if (errorRef.current === null) {
      errorRef.current = text;
      setError(text);
    }
  }, []);

  const clearError = useCallback(() => {
    errorRef.current = null;
    setError(null);
  }, []);

  const [status, setStatus] = useState<UseTerminalReturn["status"]>("disconnected");
  const [error, setError] = useState<string | null>(null);
  const [retryCount, setRetryCount] = useState(0);
  const [retryPending, setRetryPending] = useState(false);
  const [fontSize, setFontSize] = useState(fontSizeRef.current);
  // Mirrors generationRef for the UI: every connect/disconnect invalidates pending
  // clipboard work, and the toolbar must not keep offering it (F03).
  const [generation, setGeneration] = useState(0);

  const clearTimers = useCallback(() => {
    if (retryTimerRef.current !== null) {
      clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    if (resizeTimerRef.current !== null) {
      clearTimeout(resizeTimerRef.current);
      resizeTimerRef.current = null;
    }
    setRetryPending(false);
  }, []);

  const detachContainerObserver = useCallback(() => {
    containerObserverRef.current?.disconnect();
    containerObserverRef.current = null;
  }, []);

  /**
   * Handles a native paste event. The event carries the text the browser already
   * granted, so that payload is used directly: re-reading the global clipboard can
   * return something else, or fail on a permission the user already granted for this
   * paste (R02). Only the toolbar button falls back to the clipboard API.
   */
  const onNativePaste = useCallback((event: Event) => {
    event.preventDefault();
    event.stopPropagation();
    if (Date.now() < suppressNativePasteRef.current) {
      // The keyboard handler already handled this keystroke's paste (R01).
      return;
    }
    const clipboardEvent = event as ClipboardEvent;
    const payload = clipboardEvent.clipboardData?.getData("text/plain") ?? "";
    if (payload.length === 0) {
      // No payload on the event (a synthetic dispatcher, or a browser that only
      // exposes the API): fall back to the button's flow.
      shortcutsRef.current?.onPaste();
      return;
    }
    shortcutsRef.current?.onPastePayload?.(payload);
  }, []);

  const disconnect = useCallback(() => {
    manualDisconnectRef.current = true;
    // A new generation invalidates in-flight clipboard reads and confirmations.
    generationRef.current++;
    setGeneration(generationRef.current);
    pendingResizeRef.current = false;
    clearTimers();
    if (windowResizeHandlerRef.current !== null) {
      window.removeEventListener("resize", windowResizeHandlerRef.current);
      windowResizeHandlerRef.current = null;
    }
    detachContainerObserver();
    // Detach WS handlers BEFORE closing so the stale onclose can't fire later
    // and call term.writeln on the disposed terminal (xterm throws "Object has
    // been disposed") — or null the NEW ws / schedule a spurious retry.
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onopen = null;
      ws.close(1000);
    }
    terminalRef.current?.removeEventListener("paste", onNativePaste, true);
    termInstanceRef.current?.dispose();
    termInstanceRef.current = null;
    fitAddonRef.current = null;
    searchAddonRef.current = null;
    retryCountRef.current = 0;
    setRetryCount(0);
    setStatus("disconnected");
    clearError();
  }, [clearTimers, detachContainerObserver, onNativePaste, clearError]);

  /** Sends the current cols/rows to the PTY, remembering a size taken too early. */
  const sendResize = useCallback(() => {
    const term = termInstanceRef.current;
    if (!term) return;
    if (wsRef.current?.readyState !== WebSocket.OPEN) {
      // A handshake slower than the layout must not lose the initial size: the
      // pending flag is honoured as soon as the socket is ready (F06).
      pendingResizeRef.current = true;
      return;
    }
    pendingResizeRef.current = false;
    sendTerminalMessage(wsRef.current, { type: "resize", cols: term.cols, rows: term.rows });
  }, []);

  const scheduleResize = useCallback(() => {
    if (resizeTimerRef.current !== null) {
      clearTimeout(resizeTimerRef.current);
    }
    resizeTimerRef.current = setTimeout(sendResize, RESIZE_DEBOUNCE_MS);
  }, [sendResize]);

  const connect = useCallback((fromRetry = false) => {
    if (!terminalRef.current) return;

    manualDisconnectRef.current = false;
    // Every connect starts a new generation: a clipboard read or confirmation that
    // started in the previous one is no longer allowed to type.
    generationRef.current++;
    setGeneration(generationRef.current);
    clearTimers();
    // Dispose any previous terminal before opening a new one. Auto-reconnect
    // (scheduleRetry → connect(true)) reaches here with the old terminal
    // still mounted — without this, open() stacks a second terminal on the
    // same DOM node and the old term's handlers/WS keep leaking.
    if (termInstanceRef.current !== null) {
      termInstanceRef.current.dispose();
      termInstanceRef.current = null;
      fitAddonRef.current = null;
      searchAddonRef.current = null;
    }
    if (wsRef.current !== null) {
      const oldWs = wsRef.current;
      wsRef.current = null;
      // Detach handlers before close so the stale onclose can't null the new
      // WS or schedule a spurious retry after this reconnect.
      oldWs.onclose = null;
      oldWs.onmessage = null;
      oldWs.onerror = null;
      oldWs.onopen = null;
      oldWs.close(1000);
    }
    // Only a fresh (manual/initial) connect resets the retry counter; a
    // reconnect scheduled by scheduleRetry must keep counting so the
    // exponential backoff and MAX_RETRIES cap actually take effect.
    if (!fromRetry) {
      retryCountRef.current = 0;
      setRetryCount(0);
    }
    // A reconnect keeps reporting itself as a reconnect: the attempt count and the
    // new-session hint must survive the transition from "waiting for the timer" to
    // "socket in flight" (F08).
    setStatus(fromRetry ? "reconnecting" : "connecting");
    clearError();

    const term = new Terminal({
      cursorBlink: true,
      fontSize: fontSizeRef.current,
      fontFamily: "'JetBrains Mono', 'Cascadia Code', monospace",
      theme: {
        background: "#0d1117",
        foreground: "#c9d1d9",
        cursor: "#58a6ff",
        selectionBackground: "#264f78",
        black: "#484f58",
        red: "#ff7b72",
        green: "#3fb950",
        yellow: "#d29922",
        blue: "#58a6ff",
        magenta: "#bc8cff",
        cyan: "#39c5d0",
        white: "#b1bac4",
        brightBlack: "#6e7681",
        brightRed: "#ffa198",
        brightGreen: "#56d364",
        brightYellow: "#e3b341",
        brightBlue: "#79c0ff",
        brightMagenta: "#d2a8ff",
        brightCyan: "#56d4dd",
        brightWhite: "#f0f6fc",
      },
      allowProposedApi: true,
    });

    const fitAddon = new FitAddon();
    const searchAddon = new SearchAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(searchAddon);
    term.loadAddon(new WebLinksAddon());

    // Default canvas renderer (WebGL addon removed — see autoresearch log)

    term.open(terminalRef.current);
    fitAddon.fit();
    termInstanceRef.current = term;
    fitAddonRef.current = fitAddon;
    searchAddonRef.current = searchAddon;

    const scheduleRetry = () => {
      if (manualDisconnectRef.current) return;
      const attempt = retryCountRef.current + 1;
      if (attempt > MAX_RETRIES) {
        setStatus("disconnected");
        setError("Reconnection failed after maximum attempts");
        return;
      }
      retryCountRef.current = attempt;
      setRetryCount(attempt);
      setStatus("reconnecting");
      setRetryPending(true);
      const delay = BASE_DELAY_MS * 2 ** (attempt - 1);
      retryTimerRef.current = setTimeout(() => {
        retryTimerRef.current = null;
        setRetryPending(false);
        connect(true);
      }, delay);
    };

    const ws = createTerminalSocket(
      serverId,
      (msg: TerminalServerMessage) => {
        if (msg.type === "connected") {
          retryCountRef.current = 0;
          setRetryCount(0);
          setStatus("connected");
          // The PTY starts at its own default (80x24). Whatever the layout produced
          // before the handshake finished is sent now, unconditionally, so the shell
          // and the browser agree on the size from the first prompt (F06).
          sendResize();
        } else if (msg.type === "output") {
          // Follow new output only while the user is already at the bottom *and* is
          // not selecting: reading or selecting history must not be yanked away.
          const buffer = term.buffer.active;
          const pinned = buffer.viewportY >= buffer.baseY && !term.hasSelection();
          // While reading, remember the *content* at the top of the viewport, not its
          // index: once the scrollback is full, xterm trims old lines and every index
          // shifts, so restoring a number would show different text (R04). A marker
          // moves with the trimming.
          // Marker offsets are relative to the cursor line (absolute baseY + cursorY),
          // so landing on the top visible line (absolute viewportY) means this offset.
          const anchor = pinned ? null : term.registerMarker(buffer.viewportY - buffer.baseY - buffer.cursorY);
          term.write(msg.data ?? "", () => {
            if (pinned) {
              term.scrollToBottom();
              return;
            }
            if (anchor && !anchor.isDisposed) {
              term.scrollToLine(anchor.line);
            }
            anchor?.dispose();
          });
        } else if (msg.type === "disconnected") {
          term.writeln("\r\n\x1b[33m[Disconnected]\x1b[0m");
          setStatus("disconnected");
        } else if (msg.type === "error") {
          // The backend sends the reason in `data` (handler wsMessage); `message` is
          // tolerated for older frames, and the reason must be readable outside the
          // terminal either way (F05).
          const reason = msg.data || msg.message || "Unknown error";
          term.writeln(`\r\n\x1b[31m[Error: ${reason}]\x1b[0m`);
          reportError(reason);
          // A refusal at admission (revoked session, denied access) is terminal:
          // retrying with the same credentials can only fail again (F07).
          if (msg.reason === "revoked" || msg.reason === "auth" || msg.reason === "denied") {
            terminateRef.current = true;
          }
        }
      },
      (event: CloseEvent) => {
        term.writeln("\r\n\x1b[33m[Connection closed]\x1b[0m");
        wsRef.current = null;
        if (manualDisconnectRef.current) {
          setStatus("disconnected");
          return;
        }
        const verdict = classifyClose(event, terminateRef.current);
        if (verdict === "retry") {
          scheduleRetry();
          return;
        }
        // Normal end or a refusal that retrying cannot fix: stop, drop the timer and
        // report why instead of reopening a shell the server already refused (F07).
        terminateRef.current = false;
        clearTimers();
        setStatus("disconnected");
        // The reason the server sent (frame or close reason) is shown when there is
        // one; a bare abnormal close still gets a readable line (F05/F07).
        reportError(event.reason || (verdict === "revoked" ? "session revoked" : "session ended"));
      },
      (_evt: Event) => {
        setError("WebSocket error");
        // onerror is always followed by onclose, which handles retry
      },
    );
    wsRef.current = ws;

    term.onData((data: string) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        sendTerminalMessage(wsRef.current, { type: "input", data });
      }
    });

    // Terminal-local shortcuts are handled *before* xterm encodes the key, so Ctrl+F
    // opens the local search instead of sending 0x06, and paste combinations run the
    // confirmation path instead of xterm's own paste (F01, F02). Ctrl+C and Ctrl+L
    // return true: the shell keeps interrupt and readline clear-screen.
    term.attachCustomKeyEventHandler((event: KeyboardEvent) => {
      if (event.type !== "keydown") return true;
      const handlers = shortcutsRef.current;
      if (!handlers) return true;
      const key = event.key.toLowerCase();
      const mod = event.ctrlKey || event.metaKey;
      if (mod && !event.shiftKey && key === "f") {
        handlers.onSearch();
        return false;
      }
      if (mod && event.shiftKey && key === "c") {
        handlers.onCopy();
        return false;
      }
      if (mod && event.shiftKey && key === "v") {
        event.preventDefault();
        suppressNativePasteRef.current = Date.now() + PASTE_DEDUPE_MS;
        handlers.onPaste();
        return false;
      }
      if (mod && event.shiftKey && key === "l") {
        handlers.onClear();
        return false;
      }
      // Shift+Insert is the other paste combination xterm understands natively. The
      // browser fires a native paste event for the same keystroke, so it is deduped
      // below instead of being handled twice (R01).
      if (event.shiftKey && key === "insert") {
        event.preventDefault();
        suppressNativePasteRef.current = Date.now() + PASTE_DEDUPE_MS;
        handlers.onPaste();
        return false;
      }
      return true;
    });

    const handleResize = () => {
      fitAddonRef.current?.fit();
      // Debounce PTY resize messages to avoid flooding the backend
      scheduleResize();
    };

    // Remove any listener from a previous connect (auto-reconnect calls
    // connect() directly without going through disconnect), so repeated
    // reconnects don't accumulate window "resize" listeners.
    if (windowResizeHandlerRef.current !== null) {
      window.removeEventListener("resize", windowResizeHandlerRef.current);
    }
    window.addEventListener("resize", handleResize);
    windowResizeHandlerRef.current = handleResize;
    term.onResize(() => {
      // onResize fires after fit() — debounce same way
      scheduleResize();
    });

    // Native paste (Shift+Insert, context menu, middle click) has to be caught
    // before xterm's own textarea listener turns it into input, so every entry point
    // shares the confirmation flow (F02). A capture-phase listener on the terminal
    // container runs before the target's listeners; one attached to the textarea
    // itself would run after xterm's, because listeners on the same node fire in
    // registration order.
    const pasteTarget = terminalRef.current;
    pasteTarget?.addEventListener("paste", onNativePaste, true);

    // A layout change that never resizes the window (sidebar toggle, fullscreen,
    // flex reflow) still has to fit and re-sync the PTY size.
    detachContainerObserver();
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(() => {
        handleResize();
      });
      observer.observe(terminalRef.current);
      containerObserverRef.current = observer;
    }
  }, [serverId, clearTimers, detachContainerObserver, scheduleResize, clearError, reportError, sendResize]);

  useEffect(() => {
    return () => {
      disconnect();
    };
  }, [disconnect]);

  /** Applies a font size to the live terminal and remembers it. */
  const applyFontSize = useCallback((next: number) => {
    const clamped = saveFontSize(next);
    fontSizeRef.current = clamped;
    setFontSize(clamped);
    const term = termInstanceRef.current;
    if (term) {
      term.options.fontSize = clamped;
      // The character cell changed, so the viewport must be refitted and the PTY
      // told about the new cols/rows.
      fitAddonRef.current?.fit();
      scheduleResize();
    }
  }, [scheduleResize]);

  const increaseFontSize = useCallback(() => {
    applyFontSize(stepFontSize(fontSizeRef.current, 1));
  }, [applyFontSize]);

  const decreaseFontSize = useCallback(() => {
    applyFontSize(stepFontSize(fontSizeRef.current, -1));
  }, [applyFontSize]);

  const resetFontSize = useCallback(() => {
    applyFontSize(TERMINAL_FONT_SIZE_DEFAULT);
  }, [applyFontSize]);

  const search = useCallback((query: string, direction: "next" | "previous"): boolean => {
    const addon = searchAddonRef.current;
    if (!addon || query.length === 0) return false;
    // The official xterm search addon runs against the local scrollback only: no
    // search command is ever sent to the remote host (REQ-09 "搜索").
    return direction === "next"
      ? addon.findNext(query, { incremental: false })
      : addon.findPrevious(query, { incremental: false });
  }, []);

  const clearSearch = useCallback(() => {
    searchAddonRef.current?.clearDecorations();
    termInstanceRef.current?.focus();
  }, []);

  const clearTerminal = useCallback(() => {
    // Client-side display only: the remote scrollback and history are untouched.
    termInstanceRef.current?.clear();
  }, []);

  const copySelection = useCallback(async (): Promise<CopyOutcome> => {
    const term = termInstanceRef.current;
    const selection = term?.getSelection() ?? "";
    if (selection.length === 0) return { kind: "copy-empty" };
    const clipboard = navigator.clipboard;
    if (!clipboard?.writeText) return { kind: "copy-denied" };
    try {
      // Await the write: a rejected permission must not be reported as a copy (F04).
      await clipboard.writeText(selection);
    } catch {
      return { kind: "copy-denied" };
    }
    return { kind: "copied", bytes: new TextEncoder().encode(selection).length };
  }, []);

  const readClipboard = useCallback(async (): Promise<PasteOutcome> => {
    const generation = generationRef.current;
    let text = "";
    try {
      text = (await navigator.clipboard?.readText()) ?? "";
    } catch {
      // Browsers require a user gesture and may still refuse; the toolbar reports it.
      return { kind: "paste-denied" };
    }
    if (generation !== generationRef.current) {
      // The session was replaced while the read was in flight: the payload belongs to
      // the previous shell and must not be typed into this one (F03).
      return { kind: "paste-stale" };
    }
    const shape = classifyPaste(text);
    if (shape.kind === "empty") return { kind: "paste-empty" };
    if (shape.kind === "multi-line") {
      return { kind: "paste-multiline", text, lines: shape.lines, preview: shape.preview, generation };
    }
    return { kind: "paste-ready", text, lines: shape.lines, generation };
  }, []);

  const pasteText = useCallback((text: string, generation?: number) => {
    // A confirmation or late read from an earlier generation is dropped instead of
    // being typed into the current shell (F03).
    if (generation !== undefined && generation !== generationRef.current) return;
    if (wsRef.current?.readyState !== WebSocket.OPEN) return;
    // term.paste() routes through onData, so the PTY receives exactly what a
    // keyboard paste would send (including bracketed-paste markers).
    termInstanceRef.current?.paste(text);
  }, []);

  const focusTerminal = useCallback(() => {
    termInstanceRef.current?.focus();
  }, []);

  const setShortcuts = useCallback((handlers: ShortcutHandlers | null) => {
    shortcutsRef.current = handlers;
  }, []);

  const cancelRetry = useCallback(() => {
    // Abandoning a retry is a deliberate stop, not a failure: the state becomes
    // disconnected so the user can reconnect by hand. An in-flight attempt is closed
    // too, otherwise cancelling would leave a socket connecting in the background.
    manualDisconnectRef.current = true;
    clearTimers();
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onopen = null;
      ws.close(1000);
    }
    setStatus("disconnected");
    clearError();
  }, [clearTimers, clearError]);

  return {
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
  };
}
