import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useCallback, useEffect, useRef, useState } from "react";
import type { TerminalServerMessage } from "../../../types/ssh";
import { createTerminalSocket, sendTerminalMessage } from "../socket";

const MAX_RETRIES = 5;
const BASE_DELAY_MS = 2000;
const RESIZE_DEBOUNCE_MS = 300;

interface UseTerminalReturn {
  terminalRef: React.RefObject<HTMLDivElement | null>;
  status: "disconnected" | "connecting" | "connected" | "reconnecting";
  error: string | null;
  retryCount: number;
  connect: () => void;
  disconnect: () => void;
}

export function useTerminal(serverId: number): UseTerminalReturn {
  const terminalRef = useRef<HTMLDivElement | null>(null);
  const termInstanceRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const resizeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retryCountRef = useRef(0);
  const manualDisconnectRef = useRef(false);
  // handleResize is recreated per connect(); keep a ref so disconnect can
  // remove the exact listener that was added (otherwise reconnects leak
  // window "resize" listeners).
  const windowResizeHandlerRef = useRef<(() => void) | null>(null);

  const [status, setStatus] = useState<UseTerminalReturn["status"]>("disconnected");
  const [error, setError] = useState<string | null>(null);
  const [retryCount, setRetryCount] = useState(0);

  const clearTimers = useCallback(() => {
    if (retryTimerRef.current !== null) {
      clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    if (resizeTimerRef.current !== null) {
      clearTimeout(resizeTimerRef.current);
      resizeTimerRef.current = null;
    }
  }, []);

  const disconnect = useCallback(() => {
    manualDisconnectRef.current = true;
    clearTimers();
    if (windowResizeHandlerRef.current !== null) {
      window.removeEventListener("resize", windowResizeHandlerRef.current);
      windowResizeHandlerRef.current = null;
    }
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
      ws.close();
    }
    termInstanceRef.current?.dispose();
    termInstanceRef.current = null;
    fitAddonRef.current = null;
    retryCountRef.current = 0;
    setRetryCount(0);
    setStatus("disconnected");
    setError(null);
  }, [clearTimers]);

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
      oldWs.close();
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
      fontSize: 14,
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
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon());

    // Default canvas renderer (WebGL addon removed — see autoresearch log)

    term.open(terminalRef.current);
    fitAddon.fit();
    termInstanceRef.current = term;
    fitAddonRef.current = fitAddon;

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
		const delay = BASE_DELAY_MS * 2 ** (attempt - 1);
		retryTimerRef.current = setTimeout(() => {
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
          term.write(msg.data ?? "");
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

    const sendResize = () => {
      if (wsRef.current?.readyState === WebSocket.OPEN && termInstanceRef.current) {
        const { cols, rows } = termInstanceRef.current;
        sendTerminalMessage(wsRef.current, { type: "resize", cols, rows });
      }
    };

    const handleResize = () => {
      fitAddonRef.current?.fit();
      // Debounce PTY resize messages to avoid flooding the backend
      if (resizeTimerRef.current !== null) {
        clearTimeout(resizeTimerRef.current);
      }
      resizeTimerRef.current = setTimeout(sendResize, RESIZE_DEBOUNCE_MS);
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
      if (resizeTimerRef.current !== null) {
        clearTimeout(resizeTimerRef.current);
      }
      resizeTimerRef.current = setTimeout(sendResize, RESIZE_DEBOUNCE_MS);
    });
  }, [serverId, clearTimers]);

  useEffect(() => {
    return () => {
      disconnect();
    };
  }, [disconnect]);

  return { terminalRef, status, error, retryCount, connect, disconnect };
}
