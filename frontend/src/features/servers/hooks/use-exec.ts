import { type UseMutationResult, useMutation } from "@tanstack/react-query";
import type { ExecResponse } from "../../../types/ssh";
import { execCommand } from "../api";

type ExecVariables = { command: string; timeout?: number };

export function useExecCommand(
  serverId: number,
): UseMutationResult<ExecResponse, Error, ExecVariables> {
  return useMutation({
    mutationFn: ({ command, timeout }: ExecVariables) => execCommand(serverId, command, timeout),
  });
}
