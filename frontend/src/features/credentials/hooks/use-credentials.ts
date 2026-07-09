import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createCredential, deleteCredential, getCredentials, updateCredential } from "../api";

export function useCredentials() {
  return useQuery({
    queryKey: ["credentials"],
    queryFn: getCredentials,
    staleTime: 60_000,
  });
}

export function useCreateCredential() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: createCredential,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
    },
  });
}

export function useDeleteCredential() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: deleteCredential,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
    },
  });
}

export function useUpdateCredential() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, data }: { id: number; data: { username?: string; password?: string; private_key?: string } }) =>
      updateCredential(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["credentials"] });
    },
  });
}
