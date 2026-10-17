export interface ExecRequest {
  command: string;
  timeout?: number;
}

export interface ExecResponse {
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
}

export interface TerminalClientMessage {
  type: "auth" | "input" | "resize";
  data?: string;
  cols?: number;
  rows?: number;
}

export interface TerminalControlMessage {
  type: "connected" | "output" | "error" | "disconnected";
  data?: string;
  message?: string;
  session_id?: string;
  reason?: string;
}

export type TerminalServerMessage =
  | TerminalControlMessage
  | { type: "output"; data: Uint8Array };
