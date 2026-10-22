import { z } from "zod";

import { apiClient } from "../../lib/api-client";
import type { QueryContext } from "../../lib/query";

const ReleaseSchema = z.object({
  version: z.string(),
  published_at: z.string().datetime(),
  notes: z.string(),
  url: z.string().url(),
});

const UpdateStatusSchema = z.object({
  current_version: z.string(),
  latest_release: ReleaseSchema.nullable(),
  update_available: z.boolean(),
  checked_at: z.string().datetime().nullable(),
});

export type Release = z.infer<typeof ReleaseSchema>;

export async function getUpdateStatus({ signal }: QueryContext) {
  const data = await apiClient.get("/api/v1/updates", { signal, cache: "no-store" });
  return UpdateStatusSchema.parse(data);
}
