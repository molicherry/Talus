import { useSyncExternalStore } from "react";
import { getAuthSnapshot, getServerAuthSnapshot, subscribeAuth } from "../lib/auth";
import type { AuthUser } from "../lib/auth";

export type { AuthUser } from "../lib/auth";

export interface UseAuthResult {
  user: AuthUser | null;
  isAuthenticated: boolean;
  isAdmin: boolean;
  authEpoch: number;
  usagePermissionRevision: number;
  canViewUsageLogs: boolean;
}

export function useAuth(): UseAuthResult {
  const snapshot = useSyncExternalStore(subscribeAuth, getAuthSnapshot, getServerAuthSnapshot);
  return {
    user: snapshot.user,
    isAuthenticated: snapshot.user !== null,
    isAdmin: snapshot.user?.role === "admin",
    authEpoch: snapshot.authEpoch,
    usagePermissionRevision: snapshot.usagePermissionRevision,
    canViewUsageLogs: snapshot.canViewUsageLogs,
  };
}
