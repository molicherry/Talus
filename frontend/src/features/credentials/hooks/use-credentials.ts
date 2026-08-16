import { invalidateQueries, useMutation, useQuery } from "../../../lib/query";
import { createCredential, deleteCredential, getCredentials, updateCredential } from "../api";

export function useCredentials() {
  return useQuery({
    queryKey: ["credentials"],
    queryFn: getCredentials,
    staleTime: 60_000,
  });
}

export function useCreateCredential() {
  return useMutation({
    mutationFn: createCredential,
    onSuccess: () => {
      invalidateQueries(["credentials"]);
    },
  });
}

export function useDeleteCredential() {
  return useMutation({
    mutationFn: deleteCredential,
    onSuccess: () => {
      invalidateQueries(["credentials"]);
    },
  });
}

export function useUpdateCredential() {
  return useMutation({
    mutationFn: ({ id, data }: { id: number; data: { username?: string; password?: string; private_key?: string } }) =>
      updateCredential(id, data),
    onSuccess: () => {
      invalidateQueries(["credentials"]);
    },
  });
}
