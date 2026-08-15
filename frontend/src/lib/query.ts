import { useEffect, useRef, useState } from "react";

/**
 * Minimal data-fetching layer replacing @tanstack/react-query.
 *
 * The app only uses: useQuery (key/fn/staleTime/enabled/refetchInterval),
 * useMutation (fn/onSuccess/onError + per-call callbacks), and
 * invalidateQueries with prefix keys. No optimistic updates, no dependent
 * queries, no cache writes. That surface is covered here in ~150 lines.
 *
 * Semantics preserved from the old config (src/app/query-client.ts):
 *   staleTime 30s default, retry 1, refetchOnWindowFocus false (never refetched
 *   on focus — focus does nothing), 60s polling for metrics/dashboard.
 */

export type QueryKey = readonly unknown[];

interface QueryOptions<T> {
  queryKey: QueryKey;
  queryFn: () => Promise<T>;
  staleTime?: number;
  enabled?: boolean;
  refetchInterval?: number | false;
}

export interface QueryResult<T> {
  data: T | undefined;
  error: Error | null;
  isLoading: boolean;
  isFetching: boolean;
  isRefetching: boolean;
  isError: boolean;
  refetch: () => Promise<void>;
}

interface MutationOptions<TData, TVars> {
  mutationFn: (vars: TVars) => Promise<TData>;
  onSuccess?: (data: TData, vars: TVars) => void;
  onError?: (error: unknown, vars: TVars) => void;
}

export interface UseMutationResult<TData, TError, TVars> {
  data: TData | undefined;
  error: TError | null;
  isPending: boolean;
  isSuccess: boolean;
  isError: boolean;
  mutate: (
    vars: TVars,
    callbacks?: {
      onSuccess?: (data: TData, vars: TVars) => void;
      onError?: (error: unknown, vars: TVars) => void;
    },
  ) => void;
}

interface Entry<T> {
  key: string;
  queryKey?: QueryKey;
  data: T | undefined;
  error: Error | null;
  status: "idle" | "loading" | "success" | "error";
  lastFetched: number;
  listeners: Set<() => void>;
  inFlight: Promise<void> | null;
  lastQueryFn?: () => Promise<unknown>;
  lastStaleTime?: number;
}

const cache = new Map<string, Entry<unknown>>();

export function getEntry<T>(key: string): Entry<T> {
  let e = cache.get(key) as Entry<T> | undefined;
  if (!e) {
    e = {
      key,
      data: undefined,
      error: null,
      status: "idle",
      lastFetched: 0,
      listeners: new Set(),
      inFlight: null,
    };
    cache.set(key, e as Entry<unknown>);
  }
  return e;
}

export function notify(e: Entry<unknown>): void {
  e.listeners.forEach((l) => l());
}

export async function runQuery<T>(
  e: Entry<T>,
  queryFn: () => Promise<T>,
  retriesLeft = 1,
): Promise<void> {
  if (e.inFlight) return e.inFlight;
  e.status = "loading";
  notify(e);
  const attempt = async (n: number): Promise<void> => {
    try {
      const data = await queryFn();
      e.data = data;
      e.error = null;
      e.status = "success";
      e.lastFetched = Date.now();
      e.inFlight = null;
      notify(e);
    } catch (err) {
      if (n > 0) return attempt(n - 1); // one retry (old config: retry: 1)
      e.error = err instanceof Error ? err : new Error(String(err));
      e.status = "error";
      e.inFlight = null;
      notify(e);
    }
  };
  e.inFlight = attempt(retriesLeft);
  return e.inFlight;
}

function isPrefixMatch(full: readonly unknown[], prefix: readonly unknown[]): boolean {
  if (prefix.length > full.length) return false;
  for (let i = 0; i < prefix.length; i++) {
    if (!Object.is(full[i], prefix[i])) return false;
  }
  return true;
}

/**
 * Prefix-matched cache invalidation.
 * Marks every matching entry stale (so it refetches on next mount, even if no
 * component is currently subscribed) and actively refetches the ones that are
 * mounted — mirroring react-query's invalidateQueries semantics.
 */
export function invalidateQueries(prefix: QueryKey): void {
  for (const e of cache.values()) {
    if (e.lastQueryFn && e.queryKey && isPrefixMatch(e.queryKey, prefix)) {
      e.lastFetched = 0; // stale → next mount refetches regardless of staleTime
      if (e.listeners.size > 0 && e.status !== "loading") {
        void runQuery(e, e.lastQueryFn);
      }
    }
  }
}

export function useQuery<T>(opts: QueryOptions<T>): QueryResult<T> {
  const key = JSON.stringify(opts.queryKey);
  const [, forceRender] = useState(0);
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const entry = getEntry<T>(key);
  entry.queryKey = opts.queryKey;
  entry.lastQueryFn = opts.queryFn as () => Promise<unknown>;
  entry.lastStaleTime = opts.staleTime ?? 30_000;

  // Subscribe to cache notifications for this key.
  useEffect(() => {
    const e = getEntry<T>(key);
    const listener = () => forceRender((n) => n + 1);
    e.listeners.add(listener);
    return () => {
      e.listeners.delete(listener);
    };
  }, [key]);

  // Initial / enabled / staleness-driven fetch.
  const enabled = opts.enabled !== false;
  useEffect(() => {
    if (!enabled) return;
    const e = getEntry<T>(key);
    const staleTime = optsRef.current.staleTime ?? 30_000;
    if (e.status === "success" && Date.now() - e.lastFetched < staleTime) return;
    void runQuery(e, optsRef.current.queryFn);
  }, [key, enabled]);

  // Polling — must re-schedule when refetchInterval or enabled changes (e.g.
  // useMetrics polls only while the tab is visible: isVisible ? 60_000 : false).
  const interval = opts.refetchInterval;
  useEffect(() => {
    if (!interval || !enabled) return;
    const id = window.setInterval(() => {
      const e = getEntry<T>(key);
      void runQuery(e, optsRef.current.queryFn);
    }, interval);
    return () => window.clearInterval(id);
  }, [key, interval, enabled]);

  return {
    data: entry.data,
    error: entry.error,
    // A fresh (idle, no-data) query is still "loading" on its first render,
    // matching react-query — otherwise list/detail pages flash their error/
    // empty state for one frame before the fetch kicks in.
    ...computeQueryFlags(entry, enabled),
    refetch: () => runQuery(entry, opts.queryFn),
  };
}

/**
 * Pure render-flag computation for a query — extracted from useQuery so the
 * react-query-parity flags (notably the idle-vs-loading first-render rule that
 * fixes the error/empty-state flash) can be unit-tested without a React renderer.
 */
export function computeQueryFlags<T>(
  entry: Entry<T>,
  enabled: boolean,
): {
  isLoading: boolean;
  isFetching: boolean;
  isRefetching: boolean;
  isError: boolean;
} {
  return {
    isLoading:
      entry.data === undefined &&
      (entry.status === "loading" || (entry.status === "idle" && enabled)),
    isFetching: entry.status === "loading",
    isRefetching: entry.status === "loading" && entry.data !== undefined,
    isError: entry.status === "error",
  };
}

export function useMutation<TData, TVars>(
  opts: MutationOptions<TData, TVars>,
): UseMutationResult<TData, Error, TVars> {
  const [state, setState] = useState<{
    data: TData | undefined;
    error: Error | null;
    isPending: boolean;
    isSuccess: boolean;
    isError: boolean;
  }>({ data: undefined, error: null, isPending: false, isSuccess: false, isError: false });
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const mutate: UseMutationResult<TData, Error, TVars>["mutate"] = (vars, callbacks) => {
    // Reset to a clean pending state (react-query parity): clear any previous
    // error/data so a re-submit doesn't briefly show a stale error while pending.
    setState({ data: undefined, error: null, isPending: true, isSuccess: false, isError: false });
    optsRef.current
      .mutationFn(vars)
      .then(
        (data) => {
          setState({ data, error: null, isPending: false, isSuccess: true, isError: false });
          optsRef.current.onSuccess?.(data, vars);
          callbacks?.onSuccess?.(data, vars);
        },
        (err: unknown) => {
          const error = err instanceof Error ? err : new Error(String(err));
          setState({ data: undefined, error, isPending: false, isSuccess: false, isError: true });
          optsRef.current.onError?.(error, vars);
          callbacks?.onError?.(error, vars);
        },
      );
  };

  return { ...state, mutate };
}
