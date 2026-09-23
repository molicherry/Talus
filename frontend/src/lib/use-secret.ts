import { useEffect, useState } from "react";
import { apiClient } from "./api-client";

/** Secrets stay in the mounted editor, never in the shared query cache. */
export function useSecret<T>(path: string | null, parse: (value: unknown) => T) {
  const [attempt, setAttempt] = useState(0);
  const [result, setResult] = useState<{
    path: string | null;
    attempt: number;
    data?: T;
    error?: Error;
  } | null>(null);

  useEffect(() => {
    if (!path) return;
    const controller = new AbortController();
    let active = true;
    apiClient
      .get<unknown>(path, { signal: controller.signal })
      .then(parse)
      .then((data) => {
        if (active) setResult({ path, attempt, data });
      })
      .catch((error: unknown) => {
        if (active)
          setResult({
            path,
            attempt,
            error: error instanceof Error ? error : new Error(String(error)),
          });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [path, attempt, parse]);

  const current = result?.path === path && result.attempt === attempt ? result : null;
  return {
    data: current?.data,
    error: current?.error,
    isLoading: path !== null && current === null,
    retry: () => setAttempt((n) => n + 1),
  };
}
