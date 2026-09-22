import type { ApiError, ApiErrorDetail } from "../types/api";
import i18n from "../i18n";
import { clearAuthToken, getAuthToken } from "./auth";
import { classifyUnauthorized } from "./unauthorized";

/**
 * Prefix for API requests, empty when the UI is served by the backend itself.
 * Exported so non-apiClient callers (e.g. the version check) hit the same
 * origin as every other request when the two are deployed separately.
 */
export const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "";

class ApiClientError extends Error {
  status: number;
  /** Stable backend identifier, mirrored in the locales as `errors.<reason>`. */
  reason?: string;
  /** Values for the reason's placeholders ({{min}}, {{status}}, ...). */
  params?: Record<string, string | number>;
  /** Field-level validation details (422). */
  details?: ApiErrorDetail[];
  body: ApiError;

  constructor(status: number, body: ApiError) {
    // The UI shows translateApiError(err), never this string — it is a localized
    // generic so a call site that forgets the helper still cannot leak English.
    super(i18n.t("common.apiError", { status }));
    this.name = "ApiClientError";
    this.status = status;
    this.reason = body.error?.reason;
    this.params = body.error?.params;
    this.details = body.error?.details;
    this.body = body;
  }
}

/**
 * Reads the error envelope off a failed response. Falls back to a synthesized
 * one (no reason) when the body is not the expected JSON, e.g. a proxy error
 * page or a dropped connection.
 */
async function errorFromResponse(response: Response): Promise<ApiClientError> {
  let body: ApiError;
  try {
    const parsed = (await response.json()) as ApiError;
    if (!parsed?.error) throw new Error("not an error envelope");
    body = parsed;
  } catch {
    body = {
      error: {
        code: response.status,
        message: response.statusText || `HTTP ${response.status}`,
      },
    };
  }
  return new ApiClientError(response.status, body);
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = getAuthToken();
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...((options.headers as Record<string, string>) ?? {}),
  };

  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  const response = await fetch(`${API_BASE_URL}${path}`, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    const error = await errorFromResponse(response);
    // The decision (inline / clear the token / also navigate) lives in
    // lib/unauthorized.ts so it can be unit tested — in particular that a 401
    // while already on /login must NOT navigate, or the setup probe on that page
    // would reload it forever.
    const action = classifyUnauthorized(error.reason, window.location.pathname);
    if (action !== "inline") clearAuthToken();
    if (action === "clear-token-and-redirect") window.location.replace("/login");
    throw error;
  }

  if (!response.ok) {
    throw await errorFromResponse(response);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const json = await response.json();
  return (json as { data: T }).data;
}

export const apiClient = {
  get<T>(path: string, options?: RequestInit): Promise<T> {
    return request<T>(path, { ...options, method: "GET" });
  },

  post<T>(path: string, body?: unknown, options?: RequestInit): Promise<T> {
    return request<T>(path, {
      ...options,
      method: "POST",
      body: body ? JSON.stringify(body) : undefined,
    });
  },

  put<T>(path: string, body?: unknown, options?: RequestInit): Promise<T> {
    return request<T>(path, {
      ...options,
      method: "PUT",
      body: body ? JSON.stringify(body) : undefined,
    });
  },

  delete<T>(path: string, options?: RequestInit): Promise<T> {
    return request<T>(path, { ...options, method: "DELETE" });
  },
};

export { ApiClientError };
