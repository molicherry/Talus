import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: createService,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["services"] });
    },
  });
}

export function useUpdateService() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      id,
      data,
    }: {
      id: number;
      data: Parameters<typeof updateService>[1];
    }) => updateService(id, data),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["services"] });
      queryClient.invalidateQueries({ queryKey: ["services", variables.id] });
    },
  });
}

export function useDeleteService() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: deleteService,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["services"] });
    },
  });
}
