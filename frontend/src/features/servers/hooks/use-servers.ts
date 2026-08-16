import { invalidateQueries, useMutation, useQuery } from "../../../lib/query";
import { createServer, deleteServer, getServer, getServerSummaries, updateServer } from "../api";

export function useServers() {
  return useQuery({
    queryKey: ["servers"],
    queryFn: getServerSummaries,
    staleTime: 30_000,
  });
}

export function useServer(id: number) {
  return useQuery({
    queryKey: ["servers", id],
    queryFn: () => getServer(id),
    enabled: !!id,
  });
}

export function useCreateServer() {
  return useMutation({
    mutationFn: createServer,
    onSuccess: () => {
      invalidateQueries(["servers"]);
    },
  });
}

export function useUpdateServer() {
  return useMutation({
    mutationFn: ({
      id,
      data,
    }: {
      id: number;
      data: Partial<{
        name: string;
        host: string;
        port: number;
        description: string;
        notes: string;
      }>;
    }) => updateServer(id, data),
    onSuccess: (_data, variables) => {
      invalidateQueries(["servers"]);
      invalidateQueries(["servers", variables.id]);
    },
  });
}

export function useDeleteServer() {
  return useMutation({
    mutationFn: deleteServer,
    onSuccess: () => {
      invalidateQueries(["servers"]);
    },
  });
}
