/**
 * The connection state model shown in the toolbar (REQ-09 "信息与状态", "连接控制").
 *
 * The states are distinct on purpose: connecting, reconnecting, disconnected and
 * failed are different situations for the user, and a reconnect always announces a
 * *new* session - the previous shell is not restored, so the UI must not imply it.
 */

export type TerminalStatus = "disconnected" | "connecting" | "connected" | "reconnecting";

/** What the toolbar should render for one connection state. */
export interface StatusView {
  tone: "ok" | "busy" | "idle" | "error";
  /** Translation key for the status text. */
  labelKey: string;
  /** Interpolation values for the label. */
  vars: Record<string, number>;
  /** A connect (or reconnect) action is available. */
  canConnect: boolean;
  /** The connect action should read as "reconnect". */
  isRetry: boolean;
  /** An automatic retry is in flight and can be cancelled. */
  canCancelRetry: boolean;
  /** The terminal can be disconnected by the user. */
  canDisconnect: boolean;
  /** This connection is a new session, not a restored one. */
  isNewSession: boolean;
}

/**
 * Describes the toolbar state. `error` is the connection failure text: when it is
 * present the state is reported as failed instead of merely disconnected, because the
 * reason has to be readable outside the terminal (REQ-09 "失败原因在终端外可读").
 */
export function describeConnection(input: {
  status: TerminalStatus;
  retryCount: number;
  maxRetries: number;
  error: string | null;
  /**
   * True while a retry timer is pending. Kept for callers that distinguish the wait
   * from the attempt; the rendered state is the same either way (F08).
   */
  retryPending?: boolean;
}): StatusView {
  const { status, retryCount, maxRetries, error } = input;
  switch (status) {
    case "connecting":
      return {
        tone: "busy",
        labelKey: "terminal.connecting",
        vars: {},
        canConnect: false,
        isRetry: false,
        canCancelRetry: true,
        canDisconnect: false,
        isNewSession: false,
      };
    case "connected":
      return {
        tone: "ok",
        labelKey: "terminal.connected",
        vars: {},
        canConnect: false,
        isRetry: false,
        canCancelRetry: false,
        canDisconnect: true,
        isNewSession: false,
      };
    case "reconnecting":
      return {
        tone: "busy",
        labelKey: "terminal.reconnecting",
        vars: { attempt: retryCount, max: maxRetries },
        canConnect: false,
        isRetry: true,
        // A retry can be cancelled while it waits for its timer and while its socket
        // is in flight (F08).
        canCancelRetry: true,
        canDisconnect: false,
        isNewSession: true,
      };
    default:
      if (error) {
        return {
          tone: "error",
          labelKey: "terminal.failed",
          vars: {},
          canConnect: true,
          isRetry: true,
          canCancelRetry: false,
          canDisconnect: false,
          isNewSession: true,
        };
      }
      return {
        tone: "idle",
        labelKey: "terminal.disconnected",
        vars: {},
        canConnect: true,
        isRetry: retryCount > 0 || false,
        canCancelRetry: false,
        canDisconnect: false,
        isNewSession: false,
      };
  }
}

/** How a closed socket should be handled. */
export type CloseVerdict = "retry" | "ended" | "revoked";

/**
 * Classifies a socket close so the client never reopens a session the server already
 * refused (F07). A normal end (1000) and an application-level refusal (4001/4003 or a
 * reason the backend sent before closing) are final; only an abnormal drop is retried.
 * `refusal` carries the reason the last frame reported, if any.
 */
export function classifyClose(
  event: { code?: number; reason?: string },
  refusal = false,
): CloseVerdict {
  if (refusal) return "revoked";
  const reason = (event.reason ?? "").toLowerCase();
  if (reason.includes("revoke")) return "revoked";
  if (reason.includes("auth") || reason.includes("denied")) return "revoked";
  const code = event.code ?? 0;
  // 1000 = deliberate close, 1008 = policy violation (refused admission),
  // 4001 = application-level revocation.
  if (code === 1000 || code === 1008 || code === 4001) return "ended";
  return "retry";
}
