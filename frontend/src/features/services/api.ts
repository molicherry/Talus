import { z } from "zod";
import { apiClient } from "../../lib/api-client";
import { type Service, type ServiceFormValues, ServiceSchema } from "../../types/models";

export async function getServices(serverId?: number): Promise<Service[]> {
  const path = serverId ? `/api/v1/services?server_id=${serverId}` : "/api/v1/services";
  const res = await apiClient.get<Service[]>(path);
  return z.array(ServiceSchema).parse(res);
}

export async function getService(id: number): Promise<Service> {
  const res = await apiClient.get<Service>(`/api/v1/services/${id}`);
  return ServiceSchema.parse(res);
}

export async function createService(data: ServiceFormValues): Promise<Service> {
  const res = await apiClient.post<Service>("/api/v1/services", data);
  return ServiceSchema.parse(res);
}

export async function updateService(id: number, data: ServiceFormValues): Promise<Service> {
  const res = await apiClient.put<Service>(`/api/v1/services/${id}`, data);
  return ServiceSchema.parse(res);
}

export async function deleteService(id: number): Promise<void> {
  await apiClient.delete(`/api/v1/services/${id}`);
}
