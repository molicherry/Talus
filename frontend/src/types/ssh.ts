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
  type: "input" | "resize";
  data?: string;
  cols?: number;
  rows?: number;
}

export interface TerminalServerMessage {
  type: "connected" | "output" | "error" | "disconnected";
  data?: string;
  message?: string;
  session_id?: string;
  reason?: string;
}
