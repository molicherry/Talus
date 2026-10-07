import { Clipboard, FileClock, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Button } from "../../../components/ui/button";
import { Dialog } from "../../../components/ui/dialog";
import { Field, Input, Select } from "../../../components/ui/field";
import { Table, TableCard, TBody, Td, Th, THead } from "../../../components/ui/table";
import { useAuth } from "../../../hooks/use-auth";
import { usePageVisibility } from "../../../hooks/use-page-visibility";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { translateApiError } from "../../../lib/api-error";
import { useFrozenList, useUsageDetail } from "../hooks/use-usage-logs";
import { DetailCache, filterKeys, filtersFromURL, filtersToURL, FrozenPages, HISTORY_LIMIT, resolveRange, validFilters } from "../lib/session";
import { actions, authTypes, outcomes } from "../types";
import { UsageIdentityFilter } from "./usage-identity-filter";
import type { TimePreset, UsageFilters, UsageLog } from "../types";

function queryURLKey(params: URLSearchParams): string {
  const copy = new URLSearchParams(params);
  copy.delete("detail");
  copy.sort();
  return copy.toString();
}
function localInput(utc: string): string {
  const date = new Date(utc);
  if (!Number.isFinite(date.getTime())) return "";
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}
function utcInput(value: string): string { const date = new Date(value); return Number.isFinite(date.getTime()) ? date.toISOString() : ""; }
function timestamp(value?: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value;
}
function fullTimestamp(value?: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return `${date.toLocaleString(undefined, { timeZoneName: "long" })} (${value})`;
}
function resourcePath(log: UsageLog): string | null {
  if (!log.resource_id || log.action.endsWith(".delete")) return null;
  if (log.resource_type === "server") return `/servers/${log.resource_id}`;
  if (log.resource_type === "service") return `/services/${log.resource_id}/edit`;
  if (log.resource_type === "credential") return `/credentials/${log.resource_id}/edit`;
  if (log.resource_type === "api_key") return "/api-keys";
  return null;
}

export function UsageLogsPage() {
  const auth = useAuth();
  const { t } = useTranslation();
  if (!auth.canViewUsageLogs) return <div className="rounded-2xl border border-border bg-card p-8 text-center" role="alert"><h1 className="text-xl font-semibold">{t("nav.usageLogs")}</h1><p className="mt-3 text-muted-foreground">{t("usage.denied")}</p></div>;
  // Render-time identity isolation: the old account's rows never survive until
  // an effect runs. All local page/detail state belongs to this epoch instance.
  return <UsageLogsSession key={auth.authEpoch} authEpoch={auth.authEpoch} />;
}

function UsageLogsSession({ authEpoch }: { authEpoch: number }) {
  const { t, i18n } = useTranslation();
  const [params, setParams] = useSearchParams();
  const initial = useRef(filtersFromURL(params));
  const [session, setSession] = useState(() => new FrozenPages(initial.current.query, crypto.randomUUID()));
  const [preset, setPreset] = useState<TimePreset>(initial.current.preset);
  const [draft, setDraft] = useState<UsageFilters>(initial.current.query);
  const [draftPreset, setDraftPreset] = useState<TimePreset>(initial.current.preset);
  const [index, setIndex] = useState(0);
  const [formError, setFormError] = useState(false);
  const [autoCheck, setAutoCheck] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [now, setNow] = useState(Date.now());
  const detailCache = useRef(new DetailCache()).current;
  const visible = usePageVisibility();
  const list = useFrozenList(session, index, authEpoch, visible, autoCheck, preset);
  const rawDetail = params.get("detail");
  const detailID = rawDetail && /^[1-9]\d*$/.test(rawDetail) ? rawDetail : null;
  const detail = useUsageDetail(detailID, authEpoch, visible, detailCache);
  const seenURL = useRef(queryURLKey(params));
  const urlKey = queryURLKey(params);
  useEffect(() => {
    if (seenURL.current === urlKey) return;
    seenURL.current = urlKey;
    const next = filtersFromURL(params);
    setSession(new FrozenPages(next.query, crypto.randomUUID()));
    setIndex(0); setPreset(next.preset); setDraft(next.query); setDraftPreset(next.preset); setFormError(false);
  }, [urlKey, params]);
  const runningVisible = !!list.data?.items.some(log => log.outcome === "running") || detail.data?.outcome === "running";
  useEffect(() => {
    if (!visible || !runningVisible) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [visible, runningVisible]);
  const startSession = (query: UsageFilters, nextPreset: TimePreset, closeDetail = false) => {
    const next = filtersToURL(query, nextPreset, closeDetail ? null : detailID);
    seenURL.current = queryURLKey(next);
    setParams(next);
    setSession(new FrozenPages(query, crypto.randomUUID()));
    setIndex(0); setPreset(nextPreset); setDraft(query); setDraftPreset(nextPreset); setFormError(false);
  };
  const apply = (event: FormEvent) => {
    event.preventDefault();
    const query = draftPreset === "custom" ? draft : { ...draft, ...resolveRange(draftPreset) };
    if (!validFilters(query)) { setFormError(true); return; }
    startSession(query, draftPreset, true);
  };
  const refresh = () => startSession(preset === "custom" ? session.query : { ...session.query, ...resolveRange(preset) }, preset);
  const setDetail = (id: string | null) => {
    const next = new URLSearchParams(params);
    if (id) next.set("detail", id); else next.delete("detail");
    setParams(next, { preventScrollReset: true });
  };
  const copy = async (value: string) => {
    try { await navigator.clipboard.writeText(value); setAnnouncement(t("common.copied")); }
    catch { setAnnouncement(t("usage.copyFailed")); }
  };
  const filterRequest = (requestID: string) => {
    const query: UsageFilters = { from: session.query.from, to: session.query.to, page_size: session.query.page_size, request_id: requestID };
    startSession(query, preset, true);
  };
  const actionLabel = (action: string) => i18n.has(`usage.actions.${action.replaceAll(".", "_")}`) ? t(`usage.actions.${action.replaceAll(".", "_")}`) : action;
  const identity = (log: UsageLog) => log.auth_type === "api_key" ? `${log.api_key_name_snapshot || t("usage.unknown")}${log.api_key_prefix_snapshot ? ` (${log.api_key_prefix_snapshot})` : ""}` : log.username_snapshot || t("usage.unknown");
  const outcome = (log: UsageLog) => {
    if (log.source === "audit_legacy") return t("usage.legacy");
    if (log.outcome === "unknown" && log.recovery_reason) return t("usage.interrupted");
    if (log.action === "server.terminal" && log.outcome === "succeeded") return t("usage.closed");
    if (log.outcome === "running" && log.phase !== "ready") return t(log.phase === "preparing" ? "usage.preparing" : "usage.connecting");
    return t(`usage.outcomes.${log.outcome}`);
  };
  const authLabel = (log: UsageLog) => log.source === "audit_legacy" ? t("usage.legacy") : i18n.has(`usage.authTypes.${log.auth_type}`) ? t(`usage.authTypes.${log.auth_type}`) : t("usage.unknown");
  const duration = (log: UsageLog) => {
    if (log.outcome === "running") return t("usage.elapsed", { seconds: Math.max(0, Math.floor((now - Date.parse(log.started_at)) / 1000)) });
    return log.duration_ms == null ? t("usage.notCollected") : `${log.duration_ms.toLocaleString()} ms`;
  };
  const requestControl = (log: UsageLog) => log.request_id ? <div className="flex max-w-52 items-center gap-1"><button type="button" title={log.request_id} className="touch-target h-8 min-w-0 truncate text-left text-xs text-link hover:underline" onClick={() => filterRequest(log.request_id!)} aria-label={`${t("usage.filterRequestID")}: ${log.request_id}`}>{log.request_id}</button><Button variant="ghost" size="icon" aria-label={`${t("common.copy")}: ${log.request_id}`} onClick={() => void copy(log.request_id!)}><Clipboard className="h-3.5 w-3.5" /></Button></div> : <span className="text-muted-foreground">{t("usage.notCollected")}</span>;
  const resource = (log: UsageLog) => <div><span className="block max-w-48 truncate" title={log.resource_name_snapshot || undefined}>{log.resource_name_snapshot || t("usage.unknown")}</span><span className="text-xs text-muted-foreground">{log.resource_type} {log.resource_id ? `#${log.resource_id}` : ""}</span></div>;
  const detailField = (label: string, value: ReactNode) => <div className="border-b border-border py-3"><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 break-words text-sm">{value ?? t("usage.notCollected")}</dd></div>;
  const safeReason = (value?: string | null) => value ? (i18n.has(`errors.${value}`) ? t(`errors.${value}`) : /^[a-z0-9_]{1,80}$/.test(value) ? value : t("usage.unknown")) : t("usage.notCollected");
  const cursorError = list.error instanceof ApiClientError && ["cursor_expired", "invalid_cursor", "cursor_filter_mismatch"].includes(list.error.reason || "");
  const selectedDifferent = list.displayedPage !== undefined && list.displayedPage !== index;
  const stateChanged = detail.data && list.data?.items.some(row => row.id === detail.data!.id && (row.outcome !== detail.data!.outcome || row.phase !== detail.data!.phase));
  const atLimit = session.visits.length >= HISTORY_LIMIT && index === HISTORY_LIMIT - 1 && !!list.data?.has_more;

  return <div className="mx-auto max-w-7xl">
    <div className="mb-6 flex flex-wrap items-start justify-between gap-3"><div><h1 className="text-2xl font-semibold tracking-tight">{t("nav.usageLogs")}</h1><p className="mt-1 max-w-3xl text-sm text-muted-foreground">{t("usage.subtitle")}</p></div><Button variant="outline" onClick={refresh}><RefreshCw className="h-4 w-4" />{t("usage.refresh")}</Button></div>
    <form onSubmit={apply} className="mb-5 rounded-2xl border border-border bg-card p-4 shadow-card">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field label={t("usage.range")} htmlFor="usage-preset"><Select id="usage-preset" value={draftPreset} onChange={event => setDraftPreset(event.target.value as TimePreset)}>{["1h", "24h", "7d", "custom"].map(value => <option key={value} value={value}>{t(`usage.presets.${value}`)}</option>)}</Select></Field>
        {draftPreset === "custom" && <><Field label={t("usage.from")} htmlFor="usage-from"><Input id="usage-from" type="datetime-local" required value={localInput(draft.from)} onChange={event => setDraft({ ...draft, from: utcInput(event.target.value) })} /></Field><Field label={t("usage.to")} htmlFor="usage-to"><Input id="usage-to" type="datetime-local" required value={localInput(draft.to)} onChange={event => setDraft({ ...draft, to: utcInput(event.target.value) })} /></Field></>}
        <Field label={t("usage.action")} htmlFor="usage-action"><Select id="usage-action" value={draft.action || ""} onChange={event => setDraft({ ...draft, action: event.target.value || undefined })}><option value="">{t("usage.all")}</option>{actions.map(value => <option key={value} value={value}>{actionLabel(value)}</option>)}</Select></Field>
        <Field label={t("usage.outcome")} htmlFor="usage-outcome"><Select id="usage-outcome" value={draft.outcome || ""} onChange={event => setDraft({ ...draft, outcome: event.target.value || undefined })}><option value="">{t("usage.all")}</option>{outcomes.map(value => <option key={value} value={value}>{t(`usage.outcomes.${value}`)}</option>)}</Select></Field>
        <Field label={t("usage.authType")} htmlFor="usage-auth"><Select id="usage-auth" value={draft.auth_type || ""} onChange={event => setDraft({ ...draft, auth_type: event.target.value || undefined })}><option value="">{t("usage.all")}</option>{authTypes.map(value => <option key={value} value={value}>{t(`usage.authTypes.${value}`)}</option>)}</Select></Field>
        {filterKeys.filter(key => !["action", "outcome", "auth_type"].includes(key)).map(key => key === "server_id" || key === "user_id" || key === "api_key_id" ? <UsageIdentityFilter key={key} id={`usage-${key}`} kind={key === "server_id" ? "server" : key === "user_id" ? "user" : "api_key"} value={draft[key]} authEpoch={authEpoch} onChange={value => setDraft({ ...draft, [key]: value })} /> : <Field key={key} label={t(`usage.filters.${key}`)} htmlFor={`usage-${key}`} hint={key === "request_id" ? <span id="usage-request-id-help">{t("usage.requestIDHelp")}</span> : undefined}>{key === "resource_type" ? <Select id={`usage-${key}`} value={draft.resource_type || ""} onChange={event => setDraft({ ...draft, resource_type: event.target.value || undefined })}><option value="">{t("usage.all")}</option>{["server", "credential", "service", "api_key"].map(value => <option key={value} value={value}>{t(`usage.resourceTypes.${value}`)}</option>)}</Select> : <Input id={`usage-${key}`} aria-describedby={key === "request_id" ? "usage-request-id-help" : undefined} value={draft[key] || ""} maxLength={key === "request_id" ? 128 : 20} onChange={event => setDraft({ ...draft, [key]: event.target.value.trim() || undefined })} />}</Field>)}
        <Field label={t("usage.pageSize")} htmlFor="usage-size"><Select id="usage-size" value={draft.page_size} onChange={event => setDraft({ ...draft, page_size: Number(event.target.value) })}>{[25, 50, 100].map(value => <option key={value}>{value}</option>)}</Select></Field>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-4"><Button type="submit">{t("usage.apply")}</Button><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={autoCheck} onChange={event => setAutoCheck(event.target.checked)} />{t("usage.checkUpdates")}</label><span className="text-xs text-muted-foreground">{t("usage.timezone", { timezone: Intl.DateTimeFormat().resolvedOptions().timeZone })}</span></div>
      {formError && <p className="mt-3 text-sm text-danger" role="alert">{t("usage.invalidFilters")}</p>}
    </form>
    <p className="mb-3 text-xs text-muted-foreground">{timestamp(session.query.from)} — {timestamp(session.query.to)} · {t("usage.consistency")}</p>
    {list.updates && index === 0 && <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-border bg-primary-subtle p-3 text-sm" role="status"><span>{t("usage.updates")}</span><Button size="sm" onClick={refresh}>{t("usage.refresh")}</Button></div>}
    {list.probeError && autoCheck && index === 0 && <p className="mb-3 text-xs text-danger" role="status">{t("usage.probeFailed")}</p>}
    {list.error && <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-danger bg-danger-subtle p-3 text-sm" role="alert"><span>{cursorError ? t("usage.cursorExpired") : translateApiError(list.error, t)}</span><Button variant="outline" size="sm" onClick={() => cursorError ? refresh() : void list.retry()}>{cursorError ? t("usage.refresh") : t("common.retry")}</Button></div>}
    {list.rereading && <p className="mb-3 text-sm text-muted-foreground" role="status">{t("usage.rereading")}</p>}
    {list.changedPage === index && <p className="mb-3 text-sm text-muted-foreground" role="status">{t("usage.pageChanged")}</p>}
    {(list.fetching || selectedDifferent) && <p className="mb-3 text-sm text-muted-foreground" role="status">{t("usage.loadingPage", { page: index + 1 })}</p>}
    <div aria-busy={list.fetching}>
      {!list.data && list.loading ? <div className="space-y-3 rounded-2xl border border-border bg-card p-6" aria-label={t("common.loading")}>{[0, 1, 2, 3].map(value => <div key={value} className="h-10 animate-pulse rounded bg-muted" />)}</div> : list.data?.items.length === 0 ? <div className="rounded-2xl border border-border bg-card p-12 text-center"><FileClock className="mx-auto mb-3 h-8 w-8 text-muted-foreground" /><p>{t("usage.empty")}</p><p className="mt-2 text-sm text-muted-foreground">{t("usage.emptyHint")}</p></div> : list.data && <>
        <TableCard className="hidden md:block"><Table><THead><tr>{["time", "action", "actor", "resource", "outcome", "duration", "requestID"].map(key => <Th key={key}>{t(`usage.${key}`)}</Th>)}<Th>{t("common.actions")}</Th></tr></THead><TBody>{list.data.items.map(log => <tr key={log.id}><Td><span title={fullTimestamp(log.started_at)} className="whitespace-nowrap">{timestamp(log.started_at)}</span></Td><Td>{actionLabel(log.action)}</Td><Td><span className="block max-w-48 truncate" title={identity(log)}>{identity(log)}</span><span className="text-xs text-muted-foreground">{authLabel(log)}</span></Td><Td>{resource(log)}</Td><Td><span className={log.outcome === "failed" || log.outcome === "rejected" ? "text-danger" : "text-foreground"}>{outcome(log)}</span></Td><Td className="whitespace-nowrap text-xs">{duration(log)}</Td><Td>{requestControl(log)}</Td><Td><Button size="sm" variant="outline" onClick={() => setDetail(log.id)} aria-label={`${t("usage.viewDetails")} #${log.id}`}>{t("usage.viewDetails")}</Button></Td></tr>)}</TBody></Table></TableCard>
        <div className="space-y-3 md:hidden">{list.data.items.map(log => <article key={log.id} className="rounded-2xl border border-border bg-card p-4 shadow-card"><div className="mb-2 flex items-center justify-between gap-3"><span className="text-xs text-muted-foreground" title={fullTimestamp(log.started_at)}>{timestamp(log.started_at)}</span><span className="text-xs">{outcome(log)}</span></div><h2 className="font-medium">{actionLabel(log.action)}</h2><p className="mt-1 break-words text-sm">{authLabel(log)} · {identity(log)}</p><div className="my-2 text-sm">{resource(log)}</div><p className="mb-2 text-xs text-muted-foreground">{duration(log)}</p><div className="flex flex-wrap items-center justify-between gap-2">{requestControl(log)}<Button size="sm" variant="outline" onClick={() => setDetail(log.id)} aria-label={`${t("usage.viewDetails")} #${log.id}`}>{t("usage.viewDetails")}</Button></div></article>)}</div>
      </>}
    </div>
    <div className="mt-4 flex flex-wrap items-center justify-between gap-3"><span className="text-sm text-muted-foreground">{t("usage.page", { page: (list.displayedPage ?? index) + 1 })}</span><div className="flex gap-2"><Button variant="outline" disabled={index === 0 || list.fetching} onClick={() => setIndex(value => value - 1)}>{t("usage.previous")}</Button><Button variant="outline" disabled={!!list.error || list.fetching || !session.get(index)?.has_more || atLimit} onClick={() => { const next = session.next(index); if (next !== undefined) setIndex(next); }}>{t("usage.next")}</Button></div></div>
    {atLimit && <p className="mt-3 text-sm text-muted-foreground" role="status">{t("usage.historyLimit")}</p>}
    <p className="sr-only" role="status" aria-live="polite">{announcement}</p>
    <Dialog open={!!rawDetail} onClose={() => setDetail(null)} title={t("usage.detailTitle")} className="m-0 ml-auto h-dvh max-h-none w-[min(100vw,40rem)] max-w-none rounded-none border-y-0 border-r-0" contentClassName="p-6">
      {!detailID ? <p role="alert">{t("usage.invalidDetail")}</p> : <>
        <div className="mb-3 flex items-center justify-between gap-3"><span className="break-all text-xs text-muted-foreground">#{detailID}</span><Button size="sm" variant="outline" disabled={detail.fetching} onClick={() => void detail.refetch()}>{t("usage.refreshDetail")}</Button></div>
        {detail.loading && <p role="status">{t("common.loading")}</p>}
        {detail.error && <div className="mb-3 rounded-lg bg-danger-subtle p-3 text-sm text-danger" role="alert">{translateApiError(detail.error, t)}<Button className="mt-2" size="sm" variant="outline" onClick={() => void detail.refetch()}>{t("common.retry")}</Button></div>}
        {detail.data && <>
          {detail.data.source === "audit_legacy" && <p className="mb-3 text-sm text-muted-foreground">{t("usage.legacyHint")}</p>}
          {detail.data.outcome === "unknown" && detail.data.recovery_reason && <p className="mb-3 text-sm text-muted-foreground">{t("usage.interruptedHint")}</p>}
          {stateChanged && <p className="mb-3 text-sm text-link" role="status">{t("usage.stateChanged")}</p>}
          <dl>
            {detailField(t("usage.operationID"), detail.data.operation_id)}
            {detailField(t("usage.requestID"), <><span className="break-all">{detail.data.request_id || t("usage.notCollected")}</span>{detail.data.request_id && <div className="mt-2 flex flex-wrap gap-2"><Button size="sm" variant="outline" onClick={() => void copy(detail.data!.request_id!)}>{t("common.copy")}</Button><Button size="sm" variant="outline" onClick={() => filterRequest(detail.data!.request_id!)}>{t("usage.filterRequestID")}</Button></div>}</>)}
            {detailField(t("usage.action"), actionLabel(detail.data.action))}
            {detailField(t("usage.outcome"), outcome(detail.data))}
            {detailField(t("usage.startedAt"), fullTimestamp(detail.data.started_at))}
            {detailField(t("usage.finishedAt"), detail.data.finished_at ? fullTimestamp(detail.data.finished_at) : t("usage.notCollected"))}
            {detailField(t("usage.duration"), duration(detail.data))}
            {detailField(t("usage.authType"), authLabel(detail.data))}
            {detailField(t("usage.actor"), identity(detail.data))}
            {detailField(t("usage.filters.user_id"), detail.data.user_id)}
            {detailField(t("usage.filters.api_key_id"), detail.data.api_key_id)}
            {detailField(t("usage.resource"), <>{resource(detail.data)}{resourcePath(detail.data) ? <><Link className="mt-2 inline-block text-link hover:underline" to={resourcePath(detail.data)!}>{t("usage.openResource")}</Link><p className="mt-1 text-xs text-muted-foreground">{t("usage.historicalReference")}</p></> : <p className="mt-1 text-xs text-muted-foreground">{t("usage.unavailableResource")}</p>}</>)}
            {detailField(t("usage.address"), detail.data.client_address)}
            {detailField(t("usage.method"), detail.data.method)}
            {detailField(t("usage.route"), detail.data.route_pattern)}
            {detailField(t("usage.httpStatus"), detail.data.http_status)}
            {detailField(t("usage.upstreamStatus"), detail.data.upstream_status)}
            {detailField(t("usage.exitCode"), detail.data.exit_code)}
            {detailField(t("usage.errorReason"), safeReason(detail.data.error_reason))}
            {detailField(t("usage.recoveryReason"), safeReason(detail.data.recovery_reason))}
            {["timeout_seconds", "bytes_copied", "upstream_method", "close_reason", "handshake_at", "authenticated_at", "ready_at", "closed_at"].map(key => {
              const value = detail.data!.metadata?.[key];
              if (value == null) return null;
              const rendered = key.endsWith("_at") && typeof value === "string" ? fullTimestamp(value) : typeof value === "number" ? value : typeof value === "string" && value.length <= 128 ? value : t("usage.unknown");
              return <div key={key}>{detailField(t(`usage.metadata.${key}`), rendered)}</div>;
            })}
          </dl><p className="mt-4 text-xs text-muted-foreground">{t("usage.noBodies")}</p>
        </>}
      </>}
    </Dialog>
  </div>;
}
