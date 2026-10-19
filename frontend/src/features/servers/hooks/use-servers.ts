import type { ServerFormValues } from "../../../types/models";
import { invalidateQueries, useMutation, useQuery } from "../../../lib/query";
import {
  createServer,
  deleteServer,
  getServer,
  getServerSummaries,
  trustHostKey,
  updateServer,
  testServerConnection,
} from "../api";

export function useServers() {
  return useQuery({
    queryKey: ["servers"],
    queryFn: getServerSummaries,
    staleTime: 3_000,
    refetchInterval: 5_000,
  });
}

export function useServer(id: number) {
  return useQuery({
    queryKey: ["servers", id],
    queryFn: () => getServer(id),
    enabled: !!id,
    refetchInterval: 5_000,
    staleTime: 3_000,
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
      data: Partial<ServerFormValues>;
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

export function useTrustHostKey() {
  return useMutation({
    mutationFn: ({ id, fingerprint, revision }: { id: number; fingerprint: string; revision?: number }) =>
      trustHostKey(id, fingerprint, revision),
    onSuccess: (server) => {
      invalidateQueries(["servers"]);
      invalidateQueries(["servers", server.id]);
    },
  });
}

export function useTestServerConnection() {
  return useMutation({
    mutationFn: testServerConnection,
    onSettled: (_data, _error, id) => {
      // Failed host-key checks also update warnings and advance the revision.
      invalidateQueries(["servers"]);
      invalidateQueries(["servers", id]);
    },
  });
}
