import { clearAuthToken, getAuthToken } from "../lib/auth";

/**
 * Decode a JWT payload without pulling in a dependency. The token is
 * base64url-encoded JSON; only the middle segment is needed.
 */
function decodeJwtPayload<T>(token: string): T {
  const base64 = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
  const padded = base64.padEnd(Math.ceil(base64.length / 4) * 4, "=");
  // atob returns a binary/Latin-1 string — decode the bytes as UTF-8 so
  // non-ASCII payload values (e.g. usernames) survive intact.
  const bytes = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  return JSON.parse(new TextDecoder().decode(bytes)) as T;
}

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
    payload = decodeJwtPayload<JwtPayload>(token);
  } catch {
    clearAuthToken();
    return { user: null, isAuthenticated: false, isAdmin: false };
  }

  const now = Date.now();
  // A missing/malformed exp must not be treated as never-expiring:
  // NaN < now is false, which would let a bad token stay valid forever.
  if (!Number.isFinite(payload.exp) || payload.exp * 1000 < now) {
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
