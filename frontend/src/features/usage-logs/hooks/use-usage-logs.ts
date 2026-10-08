import { useEffect, useRef, useState } from "react";
import { getUsageLog, getUsageLogs } from "../api";
import { pruneQueryCache, removeQueries, useQuery } from "../../../lib/query";
import { DetailCache, FrozenPages, refreshFilters, validFilters, visibleSignature } from "../lib/session";
import type { TimePreset, UsageLog, UsageLogPage } from "../types";

export function useFrozenList(session: FrozenPages, index: number, authEpoch: number, visible: boolean, autoCheck: boolean, preset: TimePreset) {
  const valid = validFilters(session.query);
  const cached = session.get(index);
  const cursor = session.visits[index]?.cursor ?? null;
  const query = useQuery({
    queryKey: ["usage-logs", authEpoch, session.sessionID, session.canonical, cursor],
    queryFn: ({ signal }) => getUsageLogs(session.query, cursor, signal),
    enabled: valid && !cached,
    staleTime: Infinity,
  });
  const [display, setDisplay] = useState<{ session: FrozenPages; page: number; data: UsageLogPage } | null>(null);
  const [changedPage, setChangedPage] = useState<number | null>(null);
  const [, render] = useState(0);
  const [updates, setUpdates] = useState(false);
  const accepted = useRef<UsageLogPage | undefined>(undefined);
  const [probeTick, setProbeTick] = useState(0);
  const probeReady = valid && autoCheck && visible && index === 0 && !!session.get(0);
  useEffect(() => {
    accepted.current = undefined;
    setUpdates(false);
    setChangedPage(null);
  }, [session]);
  useEffect(() => {
    if (!query.data || accepted.current === query.data) return;
    accepted.current = query.data;
    if (session.store(index, query.data)) setChangedPage(index);
    setDisplay({ session, page: index, data: query.data });
    const keep = new Set(session.cachedIndices().map(i => session.visits[i].cursor));
    removeQueries(["usage-logs", authEpoch, session.sessionID], key => !keep.has(key[4] as string | null));
    render(value => value + 1);
  }, [query.data, session, index, authEpoch]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      session.prune(index);
      const keep = new Set(session.cachedIndices().map(i => session.visits[i].cursor));
      keep.add(session.visits[index]?.cursor ?? null);
      removeQueries(["usage-logs", authEpoch, session.sessionID], key => !keep.has(key[4] as string | null));
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [session, index, authEpoch]);
  // One stable probe entry per session. New relative ranges are resolved only
  // inside the request and never become the currently displayed conditions.
  const probe = useQuery({
    queryKey: ["usage-logs-probe", authEpoch, session.sessionID],
    queryFn: ({ signal }) => getUsageLogs(refreshFilters(session.query, preset), null, signal),
    enabled: false,
    staleTime: Infinity,
  });
  const probeRef = useRef(probe.refetch);
  probeRef.current = probe.refetch;
  useEffect(() => {
    if (!probeReady) return;
    const timer = window.setInterval(() => { setProbeTick(tick => tick + 1); void probeRef.current(); }, 15_000);
    return () => window.clearInterval(timer);
  }, [probeReady]);
  useEffect(() => {
    if (probeTick && probe.data && visibleSignature(probe.data.items) !== visibleSignature(session.get(0)?.items ?? [])) setUpdates(true);
  }, [probe.data, probeTick, session]);
  useEffect(() => () => {
    removeQueries(["usage-logs", authEpoch, session.sessionID]);
    removeQueries(["usage-logs-probe", authEpoch, session.sessionID]);
    session.clear();
  }, [session, authEpoch]);
  const data = cached ?? (display?.session === session ? display.data : undefined);
  return { data, displayedPage: cached ? index : display?.page, loading: !cached && query.isLoading, fetching: !cached && query.isFetching,
    error: !cached ? query.error : null, retry: query.refetch, updates, probeError: probe.error, changedPage,
    rereading: !cached && session.visits[index]?.membership !== undefined };
}

export function useUsageDetail(id: string | null, authEpoch: number, visible: boolean, cache: DetailCache) {
  const [, rerender] = useState(0);
  const [failures, setFailures] = useState(0);
  const preparedID = useRef<string | null>(null);
  const readerKey = JSON.stringify([authEpoch, id]);
  const cached = id ? cache.get(id, Date.now(), preparedID.current === readerKey) : undefined;
  if (preparedID.current !== readerKey) {
    // Discard only an unused expired entry before the new reader subscribes.
    // Otherwise stale query data could repopulate the feature cache on failure.
    if (id && !cached) pruneQueryCache({ prefix: ["usage-log", authEpoch, id], maxEntries: 0 });
    preparedID.current = readerKey;
  }
  const query = useQuery({
    queryKey: ["usage-log", authEpoch, id],
    queryFn: ({ signal }) => getUsageLog(id!, signal),
    enabled: !!id,
    staleTime: cached ? Infinity : 0,
  });
  const data: UsageLog | undefined = id ? query.data ?? cached : undefined;
  const refetch = useRef(query.refetch);
  refetch.current = query.refetch;
  useEffect(() => { setFailures(0); }, [id]);
  useEffect(() => { if (query.error) setFailures(value => value + 1); }, [query.error]);
  useEffect(() => {
    if (!query.data || !id) return;
    setFailures(0);
    cache.set(query.data, id);
    const retained = new Set(cache.ids());
    removeQueries(["usage-log", authEpoch], key => key[2] !== id && !retained.has(key[2] as string));
  }, [query.data, id, authEpoch, cache]);
  useEffect(() => {
    if (!id || !visible || data?.outcome !== "running") return;
    const timeout = window.setTimeout(() => { void refetch.current(); }, Math.min(30_000, 2000 * 2 ** Math.min(failures, 4)));
    return () => window.clearTimeout(timeout);
  }, [id, visible, data, failures, query.isFetching]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      cache.prune(id ?? undefined);
      const retained = new Set(cache.ids());
      removeQueries(["usage-log", authEpoch], key => key[2] !== id && !retained.has(key[2] as string));
      rerender(value => value + 1);
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [cache, id, authEpoch]);
  useEffect(() => () => { cache.clear(); removeQueries(["usage-log", authEpoch]); }, [cache, authEpoch]);
  return { data, loading: !!id && !data && query.isLoading, fetching: query.isFetching, error: query.error, refetch: query.refetch };
}
