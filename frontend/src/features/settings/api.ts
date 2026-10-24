import { z } from "zod";
import { apiClient } from "../../lib/api-client";

const value = z.union([z.string(), z.number(), z.boolean()]);
const source = z.enum(["default", "environment", "database"]);
const SettingSchema = z.object({
  key: z.string(),
  group: z.string(),
  kind: z.enum(["string", "number", "boolean"]),
  apply: z.enum(["agent", "next_operation", "maintenance", "live", "restart"]),
  min: z.number().optional(),
  max: z.number().optional(),
  choices: z.array(z.string()).optional(),
  value,
  effective_value: value,
  fallback_value: value,
  source,
  fallback_source: source,
  restart_required: z.boolean(),
});
const SettingsSchema = z.object({
  revision: z.number().int().positive(),
  settings: z.array(SettingSchema),
  recovery: z
    .object({ revision: z.number().int(), key: z.string() })
    .optional(),
  migration: z
    .object({ state: z.string(), imported: z.number().optional() })
    .optional(),
});
export type Setting = z.infer<typeof SettingSchema>;
export type Settings = z.infer<typeof SettingsSchema>;
export type SettingValue = z.infer<typeof value>;
export type SettingsPatch = Record<string, SettingValue | null>;

export async function getSettings(signal?: AbortSignal): Promise<Settings> {
  return SettingsSchema.parse(
    await apiClient.get("/api/v1/settings", { signal }),
  );
}
export async function saveSettings(
  revision: number,
  values: SettingsPatch,
  confirm: boolean,
  signal?: AbortSignal,
): Promise<Settings> {
  return SettingsSchema.parse(
    await apiClient.put(
      "/api/v1/settings",
      {
        revision,
        values,
        confirm_retention_change: confirm,
      },
      { signal },
    ),
  );
}

export function proposedValues(
  settings: Setting[],
  patch: SettingsPatch,
): Record<string, SettingValue> {
  return Object.fromEntries(
    settings.map((s) => [
      s.key,
      s.key in patch ? (patch[s.key] ?? s.fallback_value) : s.value,
    ]),
  );
}
export function needsRetentionConfirmation(
  settings: Setting[],
  patch: SettingsPatch,
): boolean {
  const before = proposedValues(settings, {}),
    after = proposedValues(settings, patch);
  return (
    Number(after.USAGE_LOG_RETENTION_DAYS) <
      Number(before.USAGE_LOG_RETENTION_DAYS) ||
    Number(after.USAGE_LOG_SENSITIVE_RETENTION_DAYS) <
      Number(before.USAGE_LOG_SENSITIVE_RETENTION_DAYS) ||
    (after.METRICS_RETENTION_ENABLED === true &&
      after.METRICS_RETENTION_DRY_RUN === false &&
      (before.METRICS_RETENTION_ENABLED !== true ||
        before.METRICS_RETENTION_DRY_RUN !== false))
  );
}
