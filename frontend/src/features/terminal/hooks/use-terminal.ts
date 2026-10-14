import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useCallback, useEffect, useRef, useState } from "react";
import type { TerminalServerMessage } from "../../../types/ssh";
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

/** The outcome of a copy, so the toolbar can explain what happened. */
export type CopyOutcome =
  | { kind: "copied"; bytes: number }
  | { kind: "copy-empty" }
  | { kind: "copy-denied" };

/** The outcome of reading the clipboard, including the multi-line confirmation. */
export type PasteOutcome =
  | { kind: "paste-empty" }
  | { kind: "paste-denied" }
  | { kind: "paste-ready"; text: string; lines: number }
  | { kind: "paste-multiline"; text: string; lines: number; preview: string };

export interface UseTerminalReturn {
  terminalRef: React.RefObject<HTMLDivElement | null>;
  status: "disconnected" | "connecting" | "connected" | "reconnecting";
  error: string | null;
  retryCount: number;
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
  copySelection: () => CopyOutcome;
  readClipboard: () => Promise<PasteOutcome>;
  pasteText: (text: string) => void;
  focusTerminal: () => void;
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

  const [status, setStatus] = useState<UseTerminalReturn["status"]>("disconnected");
  const [error, setError] = useState<string | null>(null);
  const [retryCount, setRetryCount] = useState(0);
  const [retryPending, setRetryPending] = useState(false);
  const [fontSize, setFontSize] = useState(fontSizeRef.current);

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

  const disconnect = useCallback(() => {
    manualDisconnectRef.current = true;
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
    termInstanceRef.current?.dispose();
    termInstanceRef.current = null;
    fitAddonRef.current = null;
    searchAddonRef.current = null;
    retryCountRef.current = 0;
    setRetryCount(0);
    setStatus("disconnected");
    setError(null);
  }, [clearTimers, detachContainerObserver]);

  /** Sends the current cols/rows to the PTY (debounced by the caller). */
  const sendResize = useCallback(() => {
    const term = termInstanceRef.current;
    if (wsRef.current?.readyState === WebSocket.OPEN && term) {
      sendTerminalMessage(wsRef.current, { type: "resize", cols: term.cols, rows: term.rows });
    }
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
    setStatus("connecting");
    setError(null);

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
        } else if (msg.type === "output") {
          // Follow new output only while the user is already at the bottom: a
          // reader who scrolled into history (or is selecting text) is not yanked
          // back by the next line (REQ-09 "尺寸与焦点").
          const buffer = term.buffer.active;
          const pinned = buffer.viewportY >= buffer.baseY;
          term.write(msg.data ?? "", () => {
            if (pinned) term.scrollToBottom();
          });
        } else if (msg.type === "disconnected") {
          term.writeln("\r\n\x1b[33m[Disconnected]\x1b[0m");
          setStatus("disconnected");
        } else if (msg.type === "error") {
          term.writeln(`\r\n\x1b[31m[Error: ${msg.message}]\x1b[0m`);
          setError(msg.message ?? "Unknown error");
        }
      },
      () => {
        term.writeln("\r\n\x1b[33m[Connection closed]\x1b[0m");
        wsRef.current = null;
        if (!manualDisconnectRef.current) {
          scheduleRetry();
        } else {
          setStatus("disconnected");
        }
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
  }, [serverId, clearTimers, detachContainerObserver, scheduleResize]);

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

  const copySelection = useCallback((): CopyOutcome => {
    const term = termInstanceRef.current;
    const selection = term?.getSelection() ?? "";
    if (selection.length === 0) return { kind: "copy-empty" };
    try {
      void navigator.clipboard?.writeText(selection);
    } catch {
      return { kind: "copy-denied" };
    }
    return { kind: "copied", bytes: new TextEncoder().encode(selection).length };
  }, []);

  const readClipboard = useCallback(async (): Promise<PasteOutcome> => {
    let text = "";
    try {
      text = (await navigator.clipboard?.readText()) ?? "";
    } catch {
      // Browsers require a user gesture and may still refuse; the toolbar reports it.
      return { kind: "paste-denied" };
    }
    const shape = classifyPaste(text);
    if (shape.kind === "empty") return { kind: "paste-empty" };
    if (shape.kind === "multi-line") {
      return { kind: "paste-multiline", text, lines: shape.lines, preview: shape.preview };
    }
    return { kind: "paste-ready", text, lines: shape.lines };
  }, []);

  const pasteText = useCallback((text: string) => {
    // term.paste() routes through onData, so the PTY receives exactly what a
    // keyboard paste would send (including bracketed-paste markers).
    termInstanceRef.current?.paste(text);
  }, []);

  const focusTerminal = useCallback(() => {
    termInstanceRef.current?.focus();
  }, []);

  const cancelRetry = useCallback(() => {
    // Abandoning a pending retry is a deliberate stop, not a failure: the state
    // becomes disconnected so the user can reconnect by hand.
    manualDisconnectRef.current = true;
    clearTimers();
    setStatus("disconnected");
    setError(null);
  }, [clearTimers]);

  return {
    terminalRef,
    status,
    error,
    retryCount,
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
  };
}
