import { apiClient } from "../../lib/api-client";
import type { LoginRequest, LoginResponse } from "../../types/api";

export function login(credentials: LoginRequest): Promise<LoginResponse> {
  return apiClient.post<LoginResponse>("/api/v1/auth/login", credentials);
}
