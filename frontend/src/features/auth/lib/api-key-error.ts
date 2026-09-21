/**
 * Classifies an API-key request failure, preferring the backend's `reason`
 * over the HTTP status class.
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
  const source = error as { status?: unknown; reason?: unknown } | null | undefined;
  const reason = typeof source?.reason === "string" ? source.reason : undefined;

  // The backend sends a stable `reason` on every error, which is more precise
  // than the status class (e.g. api_key_server_denied vs a bare 403), so it
  // wins whenever it is one we know. Unknown/missing reasons fall back to the
  // status heuristic below.
  switch (reason) {
    case "invalid_credentials":
    case "unauthorized":
      return "unauthorized";
    case "forbidden":
    case "api_key_server_denied":
    case "api_key_service_denied":
      return "forbidden";
    case "rate_limited":
      return "rateLimited";
    case "internal_error":
      return "server";
    default:
      break;
  }

  const status = source?.status;

  if (typeof status !== "number") return "network";
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 429) return "rateLimited";
  if (status >= 500) return "server";
  return "unknown";
}
