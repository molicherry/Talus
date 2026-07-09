import { getAuthToken } from "../../lib/auth";
import type { TerminalClientMessage, TerminalServerMessage } from "../../types/ssh";

export function createTerminalSocket(
  serverId: number,
  onMessage: (msg: TerminalServerMessage) => void,
  onClose: () => void,
  onError: (error: Event) => void,
): WebSocket {
  const token = getAuthToken();
  const baseUrl = import.meta.env.VITE_API_BASE_URL || window.location.origin;
  const wsBase = baseUrl.replace(/^http/, "ws");
  const ws = new WebSocket(`${wsBase}/api/v1/servers/${serverId}/terminal?token=${token}`);

  ws.onmessage = (event: MessageEvent) => {
    const msg = JSON.parse(event.data as string) as TerminalServerMessage;
    onMessage(msg);
  };
  ws.onclose = onClose;
  ws.onerror = onError;

  return ws;
}

export function sendTerminalMessage(ws: WebSocket, msg: TerminalClientMessage): void {
  ws.send(JSON.stringify(msg));
}
