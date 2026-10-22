import { z } from "zod";

import { apiClient } from "../../lib/api-client";
import type { QueryContext } from "../../lib/query";

const ReleaseSchema = z.object({
  version: z.string(),
  published_at: z.string().datetime(),
  notes: z.string(),
  url: z.string().regex(/^https:\/\/github\.com\/molicherry\/Talus\/releases\/tag\/v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$(?![\s\S])/),
});

const UpdateStatusSchema = z.object({
  current_version: z.string(),
  latest_release: ReleaseSchema.nullable(),
  update_available: z.boolean(),
  checked_at: z.string().datetime().nullable(),
});

export type Release = z.infer<typeof ReleaseSchema>;

export async function getUpdateStatus({ signal }: QueryContext) {
  const data = await apiClient.get("/api/v1/updates", { signal, cache: "default" });
  return UpdateStatusSchema.parse(data);
}
