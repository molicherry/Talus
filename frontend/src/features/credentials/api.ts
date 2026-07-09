import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import {
  type CredentialFormValues,
  type SSHCredential,
  SSHCredentialSchema,
} from "../../types/models";

export async function getCredentials(): Promise<SSHCredential[]> {
  const res = await apiClient.get<SSHCredential[]>("/api/v1/credentials");
  return z.array(SSHCredentialSchema).parse(res);
}

export async function createCredential(data: CredentialFormValues): Promise<SSHCredential> {
  const res = await apiClient.post<SSHCredential>("/api/v1/credentials", data);
  return SSHCredentialSchema.parse(res);
}

export async function updateCredential(
  id: number,
  data: { username?: string; password?: string; private_key?: string },
): Promise<SSHCredential> {
  const res = await apiClient.put<SSHCredential>(`/api/v1/credentials/${id}`, data);
  return SSHCredentialSchema.parse(res);
}

export async function deleteCredential(id: number): Promise<void> {
  await apiClient.delete(`/api/v1/credentials/${id}`);
}
