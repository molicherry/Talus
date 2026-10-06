import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import { outcomes } from "./types";
import type { UsageFilters, UsageLog, UsageLogPage } from "./types";

const id = z.string().regex(/^[1-9]\d*$/);
const optionalID = z.union([id, z.number().int().positive().max(Number.MAX_SAFE_INTEGER)]).transform(value => String(value)).nullish();
const text = z.string().nullish();
const number = z.number().nullish();
export const UsageLogSchema = z.object({
  id, operation_id: z.string(), started_at: z.string(), outcome: z.enum(outcomes), phase: z.string(), action: z.string(), resource_type: z.string(),
  source: z.enum(["operation", "audit_legacy"]), auth_type: z.string(), finished_at: text, recorded_at: text, reconciled_at: text,
  duration_ms: number, state_seq: z.number().optional(), resource_id: optionalID, resource_name_snapshot: text, server_id: optionalID,
  user_id: optionalID, username_snapshot: text, api_key_id: optionalID, api_key_name_snapshot: text, api_key_prefix_snapshot: text,
  request_id: text, method: z.string().optional(), route_pattern: z.string().optional(), http_status: number, client_address: text,
  exit_code: number, upstream_status: number, error_reason: text, recovery_reason: text, metadata: z.record(z.string(), z.unknown()).nullish(),
});
const QuerySchema = z.object({ from: z.string(), to: z.string(), page_size: z.number(),
  action: z.string().optional(), outcome: z.string().optional(), auth_type: z.string().optional(), user_id: id.optional(), api_key_id: id.optional(),
  resource_type: z.string().optional(), resource_id: id.optional(), server_id: id.optional(), request_id: z.string().optional(),
});
const PageSchema = z.object({ items: z.array(UsageLogSchema), query: QuerySchema, id_upper_bound: z.string().regex(/^\d+$/), next_cursor: z.string().nullable(), has_more: z.boolean(), consistency: z.literal("bounded_keyset") });
export async function getUsageLogs(query: UsageFilters, cursor: string | null, signal?: AbortSignal): Promise<UsageLogPage> {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) if (value !== undefined && value !== "") params.set(key, String(value));
  if (cursor) params.set("cursor", cursor);
  return PageSchema.parse(await apiClient.get(`/api/v1/usage-logs?${params}`, { signal })) as UsageLogPage;
}
export async function getUsageLog(id: string, signal?: AbortSignal): Promise<UsageLog> {
  return UsageLogSchema.parse(await apiClient.get(`/api/v1/usage-logs/${encodeURIComponent(id)}`, { signal })) as UsageLog;
}
