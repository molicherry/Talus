import { invalidateQueries, useMutation, useQuery } from "../../../lib/query";
import { createAPIKey, deleteAPIKey, listAPIKeys, revealAPIKey, rotateAPIKey, updateAPIKeyPermissions } from "../api-keys";

export function useAPIKeys(enabled = true) {
  return useQuery({
    enabled,
    queryKey: ["api-keys"],
    queryFn: listAPIKeys,
    staleTime: 30_000,
  });
}

export function useCreateAPIKey() {
  return useMutation({
    mutationFn: createAPIKey,
    onSuccess: () => {
      invalidateQueries(["api-keys"]);
    },
  });
}

export function useUpdateAPIKeyPermissions() {
  return useMutation({
    mutationFn: updateAPIKeyPermissions,
    onSuccess: () => { invalidateQueries(["api-keys"]); },
  });
}

export function useRotateAPIKey() {
  return useMutation({
    mutationFn: rotateAPIKey,
    onSuccess: () => { invalidateQueries(["api-keys"]); },
  });
}

export function useDeleteAPIKey() {
  return useMutation({
    mutationFn: deleteAPIKey,
    onSuccess: () => {
      invalidateQueries(["api-keys"]);
    },
  });
}

/** Reveal is a read of a secret; it must not touch the list cache. */
export function useRevealAPIKey() {
  return useMutation({
    mutationFn: revealAPIKey,
  });
}
