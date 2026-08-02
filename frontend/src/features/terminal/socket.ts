import { getAuthToken } from "../../lib/auth";
import type { TerminalClientMessage, TerminalServerMessage } from "../../types/ssh";

export function createTerminalSocket(
  serverId: number,
  onMessage: (msg: TerminalServerMessage) => void,
  onClose: () => void,
  onError: (error: Event) => void,
): WebSocket {
  const baseUrl = import.meta.env.VITE_API_BASE_URL || window.location.origin;
  const wsBase = baseUrl.replace(/^http/, "ws");
  // Authentication happens via the FIRST message after the handshake
  // (browsers cannot set custom headers on WebSocket connections), so the
  // token never appears in the URL or in access logs.
  const ws = new WebSocket(`${wsBase}/api/v1/servers/${serverId}/terminal`);

  ws.onmessage = (event: MessageEvent) => {
    try {
      const msg = JSON.parse(event.data as string) as TerminalServerMessage;
      onMessage(msg);
    } catch {
      // Ignore malformed frames; the terminal stays open.
    }
  };
  ws.onclose = onClose;
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
