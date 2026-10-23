import { z } from "zod";

import { apiClient } from "../../lib/api-client";

export const APIKeySchema = z.object({
  id: z.number(),
  name: z.string(),
  key_prefix: z.string(),
  scopes: z.array(z.string()).nullable().transform((scopes) => scopes ?? []),
  // `omitempty` on the backend: absent means "all servers".
  server_ids: z.array(z.number()).optional(),
  created_at: z.string(),
  // Absence identifies an older backend; never invent a revision for editing.
  permissions_revision: z.string().regex(/^[1-9]\d*$/).optional(),
  permissions_updated_at: z.string().nullish(),
});
export type APIKey = z.infer<typeof APIKeySchema>;

const CreateAPIKeyResultSchema = z.object({
  key: z.string(),
  api_key: APIKeySchema,
});

export interface CreateAPIKeyInput {
  name: string;
  scopes: string[];
  server_ids?: number[];
}

export async function listAPIKeys(): Promise<APIKey[]> {
  const res = await apiClient.get<unknown>("/api/v1/api-keys");
  return z.array(APIKeySchema).parse(res);
}

/** Returns the one-time raw key; it is never cached in the list query. */
export async function createAPIKey(input: CreateAPIKeyInput): Promise<{ key: string }> {
  const res = await apiClient.post<unknown>("/api/v1/api-keys", input);
  const parsed = CreateAPIKeyResultSchema.parse(res);
  return { key: parsed.key };
}

export async function deleteAPIKey(id: number): Promise<void> {
  await apiClient.delete(`/api/v1/api-keys/${id}`);
}

export async function revealAPIKey(id: number): Promise<string> {
  return apiClient.get<string>(`/api/v1/api-keys/${id}/reveal`);
}

export async function rotateAPIKey({ id, expected_revision }: { id: number; expected_revision: string }): Promise<{ key: string; api_key: APIKey }> {
  const result = CreateAPIKeyResultSchema.parse(await apiClient.post<unknown>(`/api/v1/api-keys/${id}/rotate`, { expected_revision }));
  return result;
}

export interface UpdateAPIKeyPermissionsInput {
  expected_revision: string;
  scopes?: string[];
  server_ids?: number[];
}

export async function updateAPIKeyPermissions({ id, ...input }: UpdateAPIKeyPermissionsInput & { id: number }): Promise<APIKey> {
  return APIKeySchema.parse(await apiClient.patch<unknown>(`/api/v1/api-keys/${id}`, input));
}
