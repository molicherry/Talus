export interface ApiResponse<T> {
  data: T;
  meta?: PaginationMeta;
}

export interface ApiError {
  error: {
    code: number;
    message: string;
    details?: Array<{ field: string; message: string }>;
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
