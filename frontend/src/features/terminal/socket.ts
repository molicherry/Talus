import { getAuthToken } from "../../lib/auth";
import type { TerminalClientMessage, TerminalServerMessage } from "../../types/ssh";

export function createTerminalSocket(
  serverId: number,
  onMessage: (msg: TerminalServerMessage) => void,
  onClose: (event: CloseEvent) => void,
  onError: (error: Event) => void,
): WebSocket {
  const baseUrl = import.meta.env.VITE_API_BASE_URL || window.location.origin;
  const wsBase = baseUrl.replace(/^http/, "ws");
  // Authentication happens via the FIRST message after the handshake
  // (browsers cannot set custom headers on WebSocket connections), so the
  // token never appears in the URL or in access logs.
  const ws = new WebSocket(`${wsBase}/api/v1/servers/${serverId}/terminal`, "talus.terminal.v2");
  ws.binaryType = "arraybuffer";

  ws.onmessage = (event: MessageEvent) => {
    if (event.data instanceof ArrayBuffer) {
      // xterm owns the streaming UTF-8 decoder: a rune may span several frames.
      onMessage({ type: "output", data: new Uint8Array(event.data) });
      return;
    }
    if (typeof event.data !== "string") return;
    try {
      const msg = JSON.parse(event.data as string) as TerminalServerMessage;
      onMessage(msg);
    } catch {
      // Ignore malformed frames; the terminal stays open.
    }
  };
  ws.onclose = (event: CloseEvent) => onClose(event);
  ws.onerror = onError;

  ws.onopen = () => {
    const token = getAuthToken();
    if (token) {
      sendTerminalMessage(ws, { type: "auth", data: token });
    } else {
      onError(new Event("no-auth-token"));
    }
  };

  return ws;
}

export function sendTerminalMessage(ws: WebSocket, msg: TerminalClientMessage): void {
  ws.send(JSON.stringify(msg));
}
