import { useEffect, useRef, useState } from "react";
import { getAuthSnapshot, subscribeAuth } from "./auth";

/** A small cache retaining the existing 30s staleness, one retry and no focus fetch. */
export type QueryKey = readonly unknown[];
export interface QueryContext { signal: AbortSignal }
export type QueryFn<T> = (context: QueryContext) => Promise<T>;

interface QueryOptions<T> {
  queryKey: QueryKey;
  queryFn: QueryFn<T>;
  staleTime?: number;
  enabled?: boolean;
  refetchInterval?: number | false;
  retry?: number;
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
  mutate: (vars: TVars, callbacks?: {
    onSuccess?: (data: TData, vars: TVars) => void;
    onError?: (error: unknown, vars: TVars) => void;
  }) => void;
}

export interface Entry<T> {
  key: string;
  queryKey?: QueryKey;
  data: T | undefined;
  error: Error | null;
  status: "idle" | "loading" | "success" | "error";
  lastFetched: number;
  lastUsed: number;
  listeners: Set<() => void>;
  inFlight: Promise<void> | null;
  controller: AbortController | null;
  generation: number;
  lastQueryFn?: QueryFn<unknown>;
  lastStaleTime?: number;
}

const cache = new Map<string, Entry<unknown>>();

export function getEntry<T>(key: string): Entry<T> {
  let entry = cache.get(key) as Entry<T> | undefined;
  if (!entry) {
    entry = {
      key, data: undefined, error: null, status: "idle", lastFetched: 0,
      lastUsed: Date.now(), listeners: new Set(), inFlight: null,
      controller: null, generation: 0,
    };
    cache.set(key, entry as Entry<unknown>);
  }
  entry.lastUsed = Date.now();
  return entry;
}

export function notify(entry: Entry<unknown>): void {
  entry.listeners.forEach((listener) => listener());
}

/** Cancellation detaches the promise immediately, even for transports ignoring signal. */
export function cancelQuery(entry: Entry<unknown>): void {
  if (!entry.inFlight && !entry.controller) return;
  entry.generation++;
  const controller = entry.controller;
  entry.controller = null;
  entry.inFlight = null;
  entry.error = null;
  entry.status = entry.data === undefined ? "idle" : "success";
  controller?.abort();
  notify(entry);
}

export function subscribeQuery(entry: Entry<unknown>, listener: () => void): () => void {
  entry.listeners.add(listener);
  entry.lastUsed = Date.now();
  return () => {
    entry.listeners.delete(listener);
    entry.lastUsed = Date.now();
    if (entry.listeners.size === 0) cancelQuery(entry);
  };
}

function isAbortError(error: unknown): boolean {
  return typeof error === "object" && error !== null && "name" in error && error.name === "AbortError";
}

export async function runQuery<T>(entry: Entry<T>, queryFn: QueryFn<T>, retriesLeft = 1): Promise<void> {
  if (entry.inFlight) return entry.inFlight;
  const generation = ++entry.generation;
  const controller = new AbortController();
  entry.controller = controller;
  entry.status = "loading";
  const current = () => entry.generation === generation && !controller.signal.aborted;
  const attempt = async (remaining: number): Promise<void> => {
    try {
      const data = await queryFn({ signal: controller.signal });
      if (!current()) return;
      entry.data = data;
      entry.error = null;
      entry.status = "success";
      entry.lastFetched = Date.now();
      entry.lastUsed = Date.now();
    } catch (error) {
      if (!current()) return;
      if (isAbortError(error)) {
        entry.error = null;
        entry.status = entry.data === undefined ? "idle" : "success";
      } else if (remaining > 0) {
        return attempt(remaining - 1);
      } else {
        entry.error = error instanceof Error ? error : new Error(String(error));
        entry.status = "error";
      }
    }
  };
  // Assign before invoking user code so synchronous completion cannot leave a stale promise.
  entry.inFlight = Promise.resolve().then(() => {
    if (current()) return attempt(retriesLeft);
  }).finally(() => {
    if (entry.generation !== generation) return;
    entry.controller = null;
    entry.inFlight = null;
    notify(entry);
  });
  notify(entry);
  return entry.inFlight;
}

function isPrefixMatch(full: QueryKey, prefix: QueryKey): boolean {
  return prefix.length <= full.length && prefix.every((value, index) => Object.is(value, full[index]));
}

export function invalidateQueries(prefix: QueryKey): void {
  for (const entry of [...cache.values()]) {
    if (!entry.queryKey || !isPrefixMatch(entry.queryKey, prefix)) continue;
    entry.lastFetched = 0;
    if (entry.lastQueryFn && entry.listeners.size > 0 && entry.status !== "loading") {
      void runQuery(entry, entry.lastQueryFn);
    }
  }
}

export function removeQueries(prefix: QueryKey = [], predicate?: (key: QueryKey) => boolean): void {
  for (const [key, entry] of [...cache]) {
    const queryKey = entry.queryKey ?? [];
    if (!isPrefixMatch(queryKey, prefix) || (predicate && !predicate(queryKey))) continue;
    cache.delete(key);
    cancelQuery(entry);
    entry.generation++;
    entry.data = undefined;
    entry.error = null;
    entry.status = "idle";
    entry.lastFetched = 0;
    notify(entry);
  }
}

interface PruneOptions {
  prefix: QueryKey;
  maxEntries: number;
  maxIdleMs?: number;
  retain?: (key: QueryKey) => boolean;
}

/** Evict only unused, unpinned entries. Active readers always remain attached. */
export function pruneQueryCache({ prefix, maxEntries, maxIdleMs, retain }: PruneOptions): void {
  const entries = [...cache.values()].filter((entry) => entry.queryKey && isPrefixMatch(entry.queryKey, prefix));
  const candidates = entries.filter((entry) => entry.listeners.size === 0 && !retain?.(entry.queryKey!))
    .sort((a, b) => a.lastUsed - b.lastUsed);
  let count = entries.length;
  const now = Date.now();
  for (const entry of candidates) {
    const expired = maxIdleMs !== undefined && now - entry.lastUsed >= maxIdleMs;
    if (count <= maxEntries && !expired) continue;
    removeQueries(prefix, (key) => key === entry.queryKey);
    count--;
  }
}

// An identity change clears every resource. A log-only permission denial must
// leave unrelated readers, requests and cache data in the same session intact.
let cacheAuthSnapshot = getAuthSnapshot();
subscribeAuth(() => {
  const next = getAuthSnapshot();
  if (next.authEpoch !== cacheAuthSnapshot.authEpoch) removeQueries();
  else if (next.usagePermissionRevision !== cacheAuthSnapshot.usagePermissionRevision) {
    for (const prefix of ["usage-logs", "usage-logs-probe", "usage-log"]) removeQueries([prefix]);
  }
  cacheAuthSnapshot = next;
});

export function useQuery<T>(opts: QueryOptions<T>): QueryResult<T> {
  const key = JSON.stringify(opts.queryKey);
  const [, forceRender] = useState(0);
  const optsRef = useRef(opts);
  optsRef.current = opts;
  const entry = getEntry<T>(key);
  entry.queryKey = opts.queryKey;
  entry.lastQueryFn = opts.queryFn as QueryFn<unknown>;
  entry.lastStaleTime = opts.staleTime ?? 30_000;

  useEffect(() => subscribeQuery(entry, () => forceRender((value) => value + 1)), [entry]);
  const enabled = opts.enabled !== false;
  useEffect(() => {
    if (!enabled) return;
    const staleTime = optsRef.current.staleTime ?? 30_000;
    if (entry.status === "success" && entry.lastFetched !== 0 && Date.now() - entry.lastFetched < staleTime) return;
    void runQuery(entry, optsRef.current.queryFn, optsRef.current.retry ?? 1);
  }, [entry, enabled]);

  const interval = opts.refetchInterval;
  useEffect(() => {
    if (!interval || !enabled) return;
    const id = window.setInterval(() => {
      void runQuery(entry, optsRef.current.queryFn, optsRef.current.retry ?? 1);
    }, interval);
    return () => window.clearInterval(id);
  }, [entry, interval, enabled]);

  return {
    data: entry.data,
    error: entry.error,
    ...computeQueryFlags(entry, enabled),
    refetch: () => runQuery(entry, opts.queryFn, opts.retry ?? 1),
  };
}

export function computeQueryFlags<T>(entry: Entry<T>, enabled: boolean): {
  isLoading: boolean; isFetching: boolean; isRefetching: boolean; isError: boolean;
} {
  return {
    isLoading: entry.data === undefined && (entry.status === "loading" || (entry.status === "idle" && enabled)),
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
