import type { ApiError, ApiErrorDetail } from "../types/api";
import i18n from "../i18n";
import { clearAuthToken, denyUsageLogs, getAuthEpoch, getAuthSnapshot, getAuthToken, subscribeAuth } from "./auth";
import { classifyUnauthorized, isUsageLogPermissionDenied, isUsageLogRequest, shouldApplyAuthorizationFailure } from "./unauthorized";

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
  const requestEpoch = getAuthEpoch();
  const usagePermissionRevision = getAuthSnapshot().usagePermissionRevision;
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

  if (response.status === 401 || response.status === 403) {
    const error = await errorFromResponse(response);
    if (shouldApplyAuthorizationFailure(requestEpoch, getAuthEpoch(), options.signal?.aborted ?? false)) {
      if (response.status === 401) {
        const action = classifyUnauthorized(error.reason, window.location.pathname);
        if (action !== "inline") clearAuthToken(requestEpoch);
        if (action === "clear-token-and-redirect") window.location.replace("/login");
      } else if (isUsageLogPermissionDenied(path, error.reason)) {
        denyUsageLogs(requestEpoch);
      }
    }
    throw error;
  }

  if (!response.ok) {
    throw await errorFromResponse(response);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const json = await response.json();
  if (isUsageLogRequest(path)) {
    const current = getAuthSnapshot();
    if (options.signal?.aborted || current.authEpoch !== requestEpoch || current.usagePermissionRevision !== usagePermissionRevision || !current.canViewUsageLogs) {
      throw new DOMException("Usage log access changed", "AbortError");
    }
  }
  return (json as { data: T }).data;
}

export interface FileTransferOptions {
  method?: "GET" | "POST" | "PUT";
  body?: Blob;
  signal?: AbortSignal;
  onProgress?: (loaded: number, total: number) => void;
  download?: boolean;
  onHeaders?: (headers: Headers, status: number) => void;
  headers?: Record<string, string>;
}

/** Binary transfers share the API client's auth, epoch and error handling. */
function transfer<T>(path: string, options: FileTransferOptions): Promise<T> {
  return new Promise((resolve, reject) => {
    const requestEpoch = getAuthEpoch();
    if (options.signal?.aborted) { reject(new DOMException("Aborted", "AbortError")); return; }
    const xhr = new XMLHttpRequest();
    let settled = false;
    let unsubscribe = () => {};
    const abort = () => xhr.abort();
    const finish = (error: unknown, data?: T) => {
      if (settled) return;
      settled = true;
      options.signal?.removeEventListener("abort", abort);
      unsubscribe();
      if (error) reject(error); else resolve(data as T);
    };
    xhr.open(options.method ?? "GET", `${API_BASE_URL}${path}`);
    xhr.responseType = "blob";
    xhr.timeout = 610_000;
    const token = getAuthToken();
    if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`);
    if (options.body) xhr.setRequestHeader("Content-Type", "application/octet-stream");
    for (const [key, value] of Object.entries(options.headers ?? {})) xhr.setRequestHeader(key, value);
    const progress = (event: ProgressEvent) => {
      if (!settled && getAuthEpoch() === requestEpoch) options.onProgress?.(event.loaded, event.lengthComputable ? event.total : 0);
    };
    if (options.download) xhr.onprogress = progress;
    else xhr.upload.onprogress = progress;
    xhr.onabort = () => finish(new DOMException("Aborted", "AbortError"));
    xhr.onerror = () => finish(new TypeError("Network request failed"));
    xhr.ontimeout = () => finish(new ApiClientError(504, { error: { code: 504, reason: "ssh_timeout", message: "" } }));
    xhr.onload = async () => {
      try {
        if (options.signal?.aborted || getAuthEpoch() !== requestEpoch) throw new DOMException("Aborted", "AbortError");
        const headers = new Headers();
        for (const line of (xhr.getAllResponseHeaders?.() ?? "").trim().split(/[\r\n]+/)) {
          const colon = line.indexOf(":"); if (colon > 0) headers.append(line.slice(0, colon).trim(), line.slice(colon + 1).trim());
        }
        const response = new Response(xhr.response, { status: xhr.status, headers });
        if (!response.ok) {
          const error = await errorFromResponse(response);
          if (xhr.status === 401 && shouldApplyAuthorizationFailure(requestEpoch, getAuthEpoch(), options.signal?.aborted ?? false)) {
            const action = classifyUnauthorized(error.reason, window.location.pathname);
            if (action !== "inline") clearAuthToken(requestEpoch);
            if (action === "clear-token-and-redirect") window.location.replace("/login");
          }
          throw error;
        }
        options.onHeaders?.(headers, xhr.status);
        const data = options.download ? xhr.response : (await response.json()).data;
        if (options.signal?.aborted || getAuthEpoch() !== requestEpoch) throw new DOMException("Aborted", "AbortError");
        finish(null, data as T);
      } catch (error) { finish(error); }
    };
    options.signal?.addEventListener("abort", abort, { once: true });
    unsubscribe = subscribeAuth(() => { if (getAuthEpoch() !== requestEpoch) abort(); });
    xhr.send(options.body);
  });
}

export const apiClient = {
  transfer,
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

  patch<T>(path: string, body: unknown, options?: RequestInit): Promise<T> {
    return request<T>(path, { ...options, method: "PATCH", body: JSON.stringify(body) });
  },

  delete<T>(path: string, options?: RequestInit): Promise<T> {
    return request<T>(path, { ...options, method: "DELETE" });
  },
};

export { ApiClientError };
