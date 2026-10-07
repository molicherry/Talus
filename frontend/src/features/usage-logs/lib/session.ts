import type { TimePreset, UsageFilters, UsageLog, UsageLogPage } from "../types";

export const PAGE_LIMIT = 20;
export const HISTORY_LIMIT = 100;
export const DETAIL_LIMIT = 50;
export const IDLE_TTL = 10 * 60_000;
export const filterKeys = ["action", "outcome", "auth_type", "user_id", "api_key_id", "resource_type", "resource_id", "server_id", "request_id"] as const;
export const idKeys = ["user_id", "api_key_id", "resource_id", "server_id"] as const;
export function canonicalFilters(query: UsageFilters): string {
  return JSON.stringify(Object.entries(query).filter(([, value]) => value !== "" && value !== undefined).sort(([a], [b]) => a.localeCompare(b)));
}
export function resolveRange(preset: TimePreset, now = Date.now()): { from: string; to: string } {
  const hours = preset === "1h" ? 1 : preset === "7d" ? 168 : 24;
  return { from: new Date(now - hours * 3600_000).toISOString(), to: new Date(now).toISOString() };
}
// PostgreSQL entity IDs are signed bigint; compare decimal strings exactly.
export function validEntityID(value: string): boolean {
  return /^[1-9]\d{0,18}$/.test(value) && (value.length < 19 || value <= "9223372036854775807");
}
export function validFilters(query: UsageFilters): boolean {
  const from = Date.parse(query.from), to = Date.parse(query.to);
  return Number.isFinite(from) && Number.isFinite(to) && from < to && to - from <= 90 * 86400_000 &&
    [25, 50, 100].includes(query.page_size) && idKeys.every(key => !query[key] || validEntityID(query[key]!)) &&
    (!query.request_id || /^[A-Za-z0-9._:-]{1,128}$/.test(query.request_id));
}
export function filtersFromURL(params: URLSearchParams, now = Date.now()): { query: UsageFilters; preset: TimePreset } {
  const value = params.get("preset");
  const preset: TimePreset = value === "1h" || value === "7d" || value === "custom" ? value : "24h";
  const range = params.get("from") && params.get("to") ? { from: params.get("from")!, to: params.get("to")! } : resolveRange(preset, now);
  const query: UsageFilters = { ...range, page_size: Number(params.get("page_size") || 25) };
  for (const key of filterKeys) if (params.get(key)) query[key] = params.get(key)!;
  return { query, preset };
}
export function filtersToURL(query: UsageFilters, preset: TimePreset, detail?: string | null): URLSearchParams {
  const params = new URLSearchParams({ from: query.from, to: query.to, page_size: String(query.page_size), preset });
  for (const key of filterKeys) if (query[key]) params.set(key, query[key]!);
  if (detail) params.set("detail", detail);
  return params;
}
function stableValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stableValue);
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, child]) => [key, stableValue(child)]));
  return value;
}
// Only durable, displayed facts participate. Heartbeats, updated_at and a
// running duration derived from now never turn into an update notification.
export function visibleSignature(items: readonly UsageLog[]): string {
  return JSON.stringify(items.map(log => stableValue({
    id: log.id, started_at: log.started_at, action: log.action, outcome: log.outcome, phase: log.phase,
    resource_type: log.resource_type, resource_id: log.resource_id, resource_name_snapshot: log.resource_name_snapshot,
    auth_type: log.auth_type, user_id: log.user_id, username_snapshot: log.username_snapshot,
    api_key_id: log.api_key_id, api_key_name_snapshot: log.api_key_name_snapshot, api_key_prefix_snapshot: log.api_key_prefix_snapshot,
    request_id: log.request_id, source: log.source, finished_at: log.finished_at,
    duration_ms: log.outcome === "running" ? null : log.duration_ms, http_status: log.http_status,
    exit_code: log.exit_code, upstream_status: log.upstream_status, error_reason: log.error_reason, recovery_reason: log.recovery_reason,
  })));
}
interface Visit { cursor: string | null; membership?: string; nextCursor?: string | null }
interface CachedPage { data: UsageLogPage; touched: number }
export class FrozenPages {
  visits: Visit[] = [{ cursor: null }];
  private pages = new Map<number, CachedPage>();
  public readonly canonical: string;
  public query: UsageFilters;
  public readonly sessionID: string;
  constructor(query: UsageFilters, sessionID: string) { this.query = query; this.sessionID = sessionID; this.canonical = canonicalFilters(query); }
  get(index: number, now = Date.now()): UsageLogPage | undefined {
    const page = this.pages.get(index);
    if (page) page.touched = now;
    return page?.data;
  }
  store(index: number, data: UsageLogPage, current = index, now = Date.now()): boolean {
    const visit = this.visits[index];
    if (!visit) throw new Error("Unknown page");
    const membership = JSON.stringify(data.items.map(item => item.id));
    const changed = visit.membership !== undefined && (visit.membership !== membership || visit.nextCursor !== data.next_cursor);
    if (changed) {
      this.visits.splice(index + 1);
      for (const key of this.pages.keys()) if (key > index) this.pages.delete(key);
    }
    visit.membership = membership;
    visit.nextCursor = data.next_cursor;
    if (index === 0) this.query = data.query;
    this.pages.set(index, { data, touched: now });
    this.prune(current, now);
    return changed;
  }
  next(index: number): number | undefined {
    const page = this.get(index);
    if (!page?.has_more || !page.next_cursor) return undefined;
    if (this.visits[index + 1]) return index + 1;
    if (this.visits.length >= HISTORY_LIMIT) return undefined;
    this.visits.push({ cursor: page.next_cursor });
    return index + 1;
  }
  prune(current: number, now = Date.now()): number[] {
    const removed: number[] = [];
    const removable = [...this.pages].filter(([index]) => index !== 0 && index !== current).sort(([, a], [, b]) => a.touched - b.touched);
    for (const [index, page] of removable) {
      if (this.pages.size <= PAGE_LIMIT && now - page.touched < IDLE_TTL) continue;
      this.pages.delete(index);
      removed.push(index);
    }
    return removed;
  }
  cachedIndices(): number[] { return [...this.pages.keys()]; }
  clear(): void { this.pages.clear(); this.visits = [{ cursor: null }]; }
}
export class DetailCache {
  private values = new Map<string, { data: UsageLog; touched: number }>();
  get(id: string, now = Date.now(), reading = false): UsageLog | undefined {
    const entry = this.values.get(id);
    if (!entry) return undefined;
    if (!reading && now - entry.touched >= IDLE_TTL) { this.values.delete(id); return undefined; }
    entry.touched = now;
    return entry.data;
  }
  set(data: UsageLog, pinned?: string, now = Date.now()): void {
    this.values.set(data.id, { data, touched: now });
    this.prune(pinned, now);
  }
  prune(pinned?: string, now = Date.now()): void {
    const oldest = [...this.values].filter(([id]) => id !== pinned).sort(([, a], [, b]) => a.touched - b.touched);
    for (const [id, value] of oldest) if (this.values.size > DETAIL_LIMIT || now - value.touched >= IDLE_TTL) this.values.delete(id);
  }
  ids(): string[] { return [...this.values.keys()]; }
  clear(): void { this.values.clear(); }
}
