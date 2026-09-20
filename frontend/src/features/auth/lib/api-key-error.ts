/**
 * Classifies an API-key request failure by HTTP status.
 *
 * Kept dependency-free so it can be unit-tested by `tests/run.sh` and reused by
 * both the list and the write paths. 401 is intentionally distinct from 403:
 * the api client already handles 401 (clears the token and redirects), while
 * 403 means "authenticated but not allowed".
 */
export type APIKeyErrorKind =
  | "unauthorized"
  | "forbidden"
  | "rateLimited"
  | "server"
  | "network"
  | "unknown";

export function apiKeyErrorKind(error: unknown): APIKeyErrorKind {
  const status = (error as { status?: unknown } | null | undefined)?.status;

  if (typeof status !== "number") return "network";
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 429) return "rateLimited";
  if (status >= 500) return "server";
  return "unknown";
}
