import { invalidateQueries, useMutation, useQuery } from "../../../lib/query";
import { createService, deleteService, getService, getServices, updateService } from "../api";

export function useServices(serverId?: number) {
  return useQuery({
    queryKey: ["services", serverId],
    queryFn: () => getServices(serverId),
    enabled: serverId === undefined ? true : !!serverId,
  });
}

export function useService(id: number) {
  return useQuery({
    queryKey: ["services", id],
    queryFn: () => getService(id),
    enabled: !!id,
  });
}

export function useCreateService() {
  return useMutation({
    mutationFn: createService,
    onSuccess: () => {
      invalidateQueries(["services"]);
    },
  });
}

export function useUpdateService() {
  return useMutation({
    mutationFn: ({
      id,
      data,
    }: {
      id: number;
      data: Parameters<typeof updateService>[1];
    }) => updateService(id, data),
    onSuccess: (_data, variables) => {
      invalidateQueries(["services"]);
      invalidateQueries(["services", variables.id]);
    },
  });
}

export function useDeleteService() {
  return useMutation({
    mutationFn: deleteService,
    onSuccess: () => {
      invalidateQueries(["services"]);
    },
  });
}
