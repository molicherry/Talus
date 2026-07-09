import { jwtDecode } from "jwt-decode";
import { clearAuthToken, getAuthToken } from "../lib/auth";

interface JwtPayload {
  uid: number;
  exp: number;
  iat: number;
  username: string;
  role: string;
}

export interface AuthUser {
  id: number;
  username: string;
  role: string;
}

export interface UseAuthResult {
  user: AuthUser | null;
  isAuthenticated: boolean;
  isAdmin: boolean;
}

export function useAuth(): UseAuthResult {
  const token = getAuthToken();

  if (!token) {
    return { user: null, isAuthenticated: false, isAdmin: false };
  }

  let payload: JwtPayload;
  try {
    payload = jwtDecode<JwtPayload>(token);
  } catch {
    clearAuthToken();
    return { user: null, isAuthenticated: false, isAdmin: false };
  }

  const now = Date.now();
  if (payload.exp * 1000 < now) {
    clearAuthToken();
    return { user: null, isAuthenticated: false, isAdmin: false };
  }

  const user: AuthUser = {
    id: payload.uid,
    username: payload.username,
    role: payload.role,
  };

  return {
    user,
    isAuthenticated: true,
    isAdmin: user.role === "admin",
  };
}
