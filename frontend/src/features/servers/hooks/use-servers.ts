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
    mutationFn: ({ id, fingerprint }: { id: number; fingerprint: string }) =>
      trustHostKey(id, fingerprint),
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
