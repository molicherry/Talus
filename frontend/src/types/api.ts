export interface ApiResponse<T> {
  data: T;
  meta?: PaginationMeta;
}

/**
 * Field-level validation error. `reason` is the stable identifier the backend
 * sends (mirrored in the locales as `errors.<reason>`), `params` carries the
 * values that go into it ({{min}}/{{max}}/...), and `message` is the English
 * fallback for logs and API agents.
 */
export interface ApiErrorDetail {
  field: string;
  reason?: string;
  message: string;
  params?: Record<string, string | number>;
}

export interface ApiError {
  error: {
    code: number;
    reason?: string;
    message: string;
    params?: Record<string, string | number>;
    details?: ApiErrorDetail[];
    request_id?: string;
  };
}

export interface PaginationMeta {
  total: number;
  page: number;
  per_page: number;
}

export interface LoginRequest {
  username: string;
  password: string;
}

export interface LoginResponse {
  token: string;
}
