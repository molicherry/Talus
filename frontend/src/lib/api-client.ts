import type { ApiError } from "../types/api";
import i18n from "../i18n";
import { clearAuthToken, getAuthToken } from "./auth";

const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "";

class ApiClientError extends Error {
  status: number;
  body: ApiError;

  constructor(status: number, body: ApiError) {
    super(body.error?.message ?? i18n.t("common.apiError", { status }));
    this.name = "ApiClientError";
    this.status = status;
    this.body = body;
  }
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

  const response = await fetch(`${BASE_URL}${path}`, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    // Don't redirect for login/register endpoints — the error should be shown inline
    if (!path.startsWith("/api/v1/auth/")) {
      clearAuthToken();
      window.location.href = "/login";
    }
    throw new ApiClientError(401, {
      error: { code: 401, message: "Unauthorized" },
    });
  }

  if (!response.ok) {
    let body: ApiError;
    try {
      body = await response.json();
    } catch {
      body = {
        error: {
          code: response.status,
          message: response.statusText,
        },
      };
    }
    throw new ApiClientError(response.status, body);
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
