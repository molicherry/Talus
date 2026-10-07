export const outcomes = ["running", "succeeded", "failed", "rejected", "cancelled", "unknown"] as const;
export const authTypes = ["jwt", "api_key", "unauthenticated", "legacy_unknown"] as const;
export const actions = [
  "server.create", "server.update", "server.delete", "credential.create", "credential.update", "credential.delete",
  "service.create", "service.update", "service.delete", "api_key.create", "api_key.delete",
  "credential.reveal", "api_key.reveal", "service.credentials", "server.host_key.trust", "server.exec", "service.relay", "server.terminal",
] as const;
export type Outcome = typeof outcomes[number];
export interface UsageLog {
  id: string;
  operation_id: string;
  started_at: string;
  recorded_at?: string;
  finished_at?: string | null;
  duration_ms?: number | null;
  outcome: Outcome;
  phase: string;
  state_seq?: number;
  action: string;
  resource_type: string;
  resource_id?: string | null;
  resource_name_snapshot?: string | null;
  server_id?: string | null;
  auth_type: string;
  user_id?: string | null;
  username_snapshot?: string | null;
  api_key_id?: string | null;
  api_key_name_snapshot?: string | null;
  api_key_prefix_snapshot?: string | null;
  request_id?: string | null;
  method?: string;
  route_pattern?: string;
  http_status?: number | null;
  client_address?: string | null;
  source: "operation" | "audit_legacy";
  exit_code?: number | null;
  upstream_status?: number | null;
  error_reason?: string | null;
  recovery_reason?: string | null;
  reconciled_at?: string | null;
  metadata?: Record<string, unknown> | null;
}
export interface UsageFilters {
  from: string;
  to: string;
  page_size: number;
  action?: string;
  outcome?: string;
  auth_type?: string;
  user_id?: string;
  api_key_id?: string;
  resource_type?: string;
  resource_id?: string;
  server_id?: string;
  request_id?: string;
}
export interface UsageLogPage {
  items: UsageLog[];
  query: UsageFilters;
  id_upper_bound: string;
  next_cursor: string | null;
  has_more: boolean;
  consistency: "bounded_keyset";
}
export type TimePreset = "1h" | "24h" | "7d" | "custom";

export type UsageIdentityKind = "server" | "user" | "api_key";
export interface UsageIdentityOption {
  id: string;
  name: string;
  prefix?: string;
  deleted: boolean;
}
export interface UsageIdentityOptions {
  items: UsageIdentityOption[];
  has_more: boolean;
}
