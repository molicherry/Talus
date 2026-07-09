import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import { type Server, ServerSchema } from "../../types/models";

export async function getServersWithMetrics(): Promise<Server[]> {
  const res = await apiClient.get<Server[]>("/api/v1/servers");
  return z.array(ServerSchema).parse(res);
}
