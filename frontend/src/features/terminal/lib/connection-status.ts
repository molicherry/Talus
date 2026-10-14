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
  /** True while a retry timer is pending. */
  retryPending?: boolean;
}): StatusView {
  const { status, retryCount, maxRetries, error, retryPending = false } = input;
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
        // A pending retry can still be cancelled before it fires.
        canCancelRetry: retryPending,
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
