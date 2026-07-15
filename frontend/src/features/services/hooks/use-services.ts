import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createService, getServices } from "../api";

export function useServices(serverId?: number) {
  return useQuery({
    queryKey: ["services", serverId],
    queryFn: () => getServices(serverId),
    enabled: serverId === undefined ? true : !!serverId,
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
