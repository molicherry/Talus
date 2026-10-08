const TOKEN_KEY = "auth_token";
const MAX_TIMER_DELAY = 2_147_483_647;

export interface AuthUser {
  id: number;
  username: string;
  role: string;
}

export interface AuthSnapshot {
  readonly token: string | null;
  readonly user: AuthUser | null;
  readonly authEpoch: number;
  /** Log permission changes are independent of the authenticated identity. */
  readonly usagePermissionRevision: number;
  readonly canViewUsageLogs: boolean;
}

interface JwtPayload {
  uid: number;
  username: string;
  role: string;
  exp: number;
}

const emptySnapshot: AuthSnapshot = {
  token: null,
  user: null,
  authEpoch: 0,
  usagePermissionRevision: 0,
  canViewUsageLogs: false,
};
let snapshot = emptySnapshot;
let initialized = false;
let expiresAt: number | null = null;
let expiryTimer: ReturnType<typeof setTimeout> | undefined;
const listeners = new Set<() => void>();

function readToken(): string | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage.getItem(TOKEN_KEY);
  } catch {
    return snapshot.token;
  }
}

function persistToken(token: string | null): void {
  if (typeof window === "undefined") return;
  try {
    if (token === null) window.localStorage.removeItem(TOKEN_KEY);
    else window.localStorage.setItem(TOKEN_KEY, token);
  } catch {
    // A blocked storage area must not prevent an in-memory session or logout.
  }
}

function decodeToken(token: string): { user: AuthUser; expiresAt: number } | null {
  try {
    const base64 = token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    const padded = base64.padEnd(Math.ceil(base64.length / 4) * 4, "=");
    const bytes = Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
    const payload = JSON.parse(new TextDecoder().decode(bytes)) as JwtPayload;
    const expiry = payload.exp * 1_000;
    if (!Number.isFinite(payload.exp) || !Number.isFinite(expiry) || expiry <= Date.now()) return null;
    if (!Number.isInteger(payload.uid) || payload.uid <= 0 || typeof payload.username !== "string" || typeof payload.role !== "string") return null;
    return {
      user: { id: payload.uid, username: payload.username, role: payload.role },
      expiresAt: expiry,
    };
  } catch {
    return null;
  }
}

function scheduleExpiry(): void {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer);
  expiryTimer = undefined;
  if (expiresAt === null) return;
  const epoch = snapshot.authEpoch;
  expiryTimer = setTimeout(() => {
    expiryTimer = undefined;
    if (snapshot.authEpoch !== epoch || expiresAt === null) return;
    if (expiresAt <= Date.now()) clearAuthToken(epoch);
    else scheduleExpiry();
  }, Math.min(MAX_TIMER_DELAY, Math.max(0, expiresAt - Date.now())));
}

function emit(): void {
  for (const listener of [...listeners]) listener();
}

function replaceToken(token: string | null, forceEpoch = false): void {
  const decoded = token === null ? null : decodeToken(token);
  const nextToken = decoded ? token : null;
  if (token !== null && !decoded) persistToken(null);
  if (nextToken === snapshot.token && !forceEpoch) return;
  expiresAt = decoded?.expiresAt ?? null;
  snapshot = {
    token: nextToken,
    user: decoded?.user ?? null,
    authEpoch: snapshot.authEpoch + 1,
    usagePermissionRevision: snapshot.usagePermissionRevision + 1,
    canViewUsageLogs: decoded?.user.role === "admin",
  };
  scheduleExpiry();
  emit();
}

function initialize(): void {
  if (initialized) return;
  initialized = true;
  const token = readToken();
  const decoded = token === null ? null : decodeToken(token);
  if (token && !decoded) persistToken(null);
  snapshot = {
    token: decoded ? token : null,
    user: decoded?.user ?? null,
    authEpoch: 0,
    usagePermissionRevision: 0,
    canViewUsageLogs: decoded?.user.role === "admin",
  };
  expiresAt = decoded?.expiresAt ?? null;
  scheduleExpiry();
  if (typeof window !== "undefined") {
    window.addEventListener("storage", (event) => {
      if (event.key !== TOKEN_KEY && event.key !== null) return;
      replaceToken(readToken());
    });
    // Background tabs can delay timers. Recheck before displaying them again.
    const recheck = () => {
      replaceToken(readToken());
      if (expiresAt !== null && expiresAt <= Date.now()) clearAuthToken();
    };
    window.addEventListener("focus", recheck);
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") recheck();
    });
  }
}

export function getAuthSnapshot(): AuthSnapshot {
  initialize();
  return snapshot;
}

export function getServerAuthSnapshot(): AuthSnapshot {
  return emptySnapshot;
}

export function subscribeAuth(listener: () => void): () => void {
  initialize();
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getAuthEpoch(): number {
  return getAuthSnapshot().authEpoch;
}

export function getAuthToken(): string | null {
  initialize();
  if (expiresAt !== null && expiresAt <= Date.now()) clearAuthToken();
  return snapshot.token;
}

export function setAuthToken(token: string): void {
  initialize();
  persistToken(token);
  // A login, including a same-token login, establishes a new cache session.
  replaceToken(token, true);
}

/** Optional epoch makes a delayed response unable to revoke a newer session. */
export function clearAuthToken(expectedEpoch?: number): boolean {
  initialize();
  if (expectedEpoch !== undefined && expectedEpoch !== snapshot.authEpoch) return false;
  persistToken(null);
  replaceToken(null);
  return true;
}

/** A forbidden log request revokes only this session's log access, not login. */
export function denyUsageLogs(expectedEpoch: number): boolean {
  initialize();
  if (expectedEpoch !== snapshot.authEpoch) return false;
  if (!snapshot.canViewUsageLogs) return true;
  snapshot = { ...snapshot, usagePermissionRevision: snapshot.usagePermissionRevision + 1, canViewUsageLogs: false };
  emit();
  return true;
}
