import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import { useCallback, useEffect, useRef, useState } from "react";
import type { TerminalServerMessage } from "../../../types/ssh";
import { createTerminalSocket, sendTerminalMessage } from "../socket";

interface UseTerminalReturn {
  terminalRef: React.RefObject<HTMLDivElement | null>;
  status: "disconnected" | "connecting" | "connected";
  error: string | null;
  connect: () => void;
  disconnect: () => void;
}

export function useTerminal(serverId: number): UseTerminalReturn {
  const terminalRef = useRef<HTMLDivElement | null>(null);
  const termInstanceRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<"disconnected" | "connecting" | "connected">("disconnected");
  const [error, setError] = useState<string | null>(null);

  const disconnect = useCallback(() => {
    wsRef.current?.close();
    termInstanceRef.current?.dispose();
    termInstanceRef.current = null;
    fitAddonRef.current = null;
    setStatus("disconnected");
  }, []);

  const connect = useCallback(() => {
    if (!terminalRef.current) return;

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

    term.open(terminalRef.current);
    fitAddon.fit();
    termInstanceRef.current = term;
    fitAddonRef.current = fitAddon;

    const ws = createTerminalSocket(
      serverId,
      (msg: TerminalServerMessage) => {
        if (msg.type === "connected") {
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
        setStatus("disconnected");
        term.writeln("\r\n\x1b[33m[Connection closed]\x1b[0m");
      },
      (_evt: Event) => {
        setError("WebSocket error");
      },
    );
    wsRef.current = ws;

    term.onData((data: string) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        sendTerminalMessage(wsRef.current, { type: "input", data });
      }
    });

    const handleResize = () => {
      fitAddon.fit();
      if (wsRef.current?.readyState === WebSocket.OPEN && termInstanceRef.current) {
        const { cols, rows } = termInstanceRef.current;
        sendTerminalMessage(wsRef.current, { type: "resize", cols, rows });
      }
    };

    window.addEventListener("resize", handleResize);
    term.onResize(({ cols, rows }: { cols: number; rows: number }) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        sendTerminalMessage(wsRef.current, { type: "resize", cols, rows });
      }
    });
  }, [serverId]);

  useEffect(() => {
    return () => {
      disconnect();
    };
  }, [disconnect]);

  return { terminalRef, status, error, connect, disconnect };
}
