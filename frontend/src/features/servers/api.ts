import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import { type Server, type ServerFormValues, type ServerSummary, ServerSchema, ServerSummarySchema } from "../../types/models";
import type { ExecResponse } from "../../types/ssh";

const ExecResponseSchema = z.object({
  stdout: z.string(),
  stderr: z.string(),
  exit_code: z.number(),
  duration_ms: z.number(),
});

// getServerSummaries fetches the lightweight server list used by the servers
// page and server-select dropdowns (GET /api/v1/servers/summary).
export async function getServerSummaries(): Promise<ServerSummary[]> {
	const res = await apiClient.get<ServerSummary[]>("/api/v1/servers/summary");
	return z.array(ServerSummarySchema).parse(res);
}

export async function getServer(id: number): Promise<Server> {
  const res = await apiClient.get<Server>(`/api/v1/servers/${id}`);
  return ServerSchema.parse(res);
}

export async function createServer(data: ServerFormValues): Promise<Server> {
  const res = await apiClient.post<Server>("/api/v1/servers", data);
  return ServerSchema.parse(res);
}

export async function updateServer(id: number, data: Partial<ServerFormValues>): Promise<Server> {
  const res = await apiClient.put<Server>(`/api/v1/servers/${id}`, data);
  return ServerSchema.parse(res);
}

export async function deleteServer(id: number): Promise<void> {
  await apiClient.delete(`/api/v1/servers/${id}`);
}

export async function execCommand(
  serverId: number,
  command: string,
  timeout?: number,
): Promise<ExecResponse> {
  const res = await apiClient.post<ExecResponse>(`/api/v1/servers/${serverId}/exec`, {
    command,
    timeout,
  });
  return ExecResponseSchema.parse(res);
}
