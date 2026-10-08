import assert from "node:assert/strict";
import { canonicalFilters, DetailCache, filterKeys, filtersFromURL, filtersToURL, FrozenPages, HISTORY_LIMIT, IDLE_TTL, refreshFilters, resolveRange, validDetailID, validEntityID, validFilters, visibleSignature } from "./.compiled-usage-session.mjs";

const realNow = Date.now;
Date.now = () => 0;
const query = { from: "2026-10-01T00:00:00.000Z", to: "2026-10-02T00:00:00.000Z", page_size: 25 };
const log = (id, extra = {}) => ({ id: String(id), operation_id: `op-${id}`, started_at: "2026-10-01T12:00:00.123456Z", action: "server.exec", outcome: "running", phase: "ready", resource_type: "server", auth_type: "jwt", source: "operation", ...extra });
const page = (id, next = null) => ({ items: [log(id)], query, id_upper_bound: "9007199254741999", next_cursor: next, has_more: !!next, consistency: "bounded_keyset" });
const session = new FrozenPages(query, "session-a");
const canonical = session.canonical;
session.store(0, page("9007199254741991", "opaque-microseconds-cursor"), 0, 0);
assert.equal(session.next(0), 1);
assert.equal(session.visits[1].cursor, "opaque-microseconds-cursor");
assert.equal(session.query.from, query.from);
assert.equal(session.query.to, query.to);
assert.equal(session.canonical, canonical);
session.store(1, page("9007199254741990", "opaque-2"), 1, 1);
assert.equal(session.next(1), 2);
assert.strictEqual(session.get(0, 500_000).items[0].id, "9007199254741991");
assert.equal(session.get(0).items[0].started_at, "2026-10-01T12:00:00.123456Z");
console.log("  ok: pages keep exact IDs, opaque cursors, microseconds and frozen query");

const large = new FrozenPages(query, "large");
for (let index = 0; index < HISTORY_LIMIT; index++) {
  large.store(index, page(1000 - index, `cursor-${index + 1}`), index, index);
  if (index < HISTORY_LIMIT - 1) assert.equal(large.next(index), index + 1);
}
assert.equal(large.cachedIndices().length, 20);
assert.ok(large.cachedIndices().includes(0));
assert.ok(large.cachedIndices().includes(HISTORY_LIMIT - 1));
assert.equal(large.get(1), undefined);
assert.equal(large.visits[1].cursor, "cursor-1");
assert.equal(large.next(HISTORY_LIMIT - 1), undefined);
assert.equal(large.visits.length, HISTORY_LIMIT);
large.prune(HISTORY_LIMIT - 1, IDLE_TTL + 10_000);
assert.deepEqual(large.cachedIndices().sort((a, b) => a - b), [0, HISTORY_LIMIT - 1]);
assert.equal(large.store(1, page(998, "new-next"), 1, IDLE_TTL + 20_000), true);
assert.equal(large.visits.length, 2);
assert.equal(large.visits[1].cursor, "cursor-1");
assert.equal(large.next(1), 2);
assert.equal(large.visits[2].cursor, "new-next");
assert.equal(large.get(99), undefined);
console.log("  ok: 20-page LRU pins first/current, idle TTL and 100-cursor limit; reread truncates downstream");

const same = new FrozenPages(query, "same");
same.store(0, page(5, "one"), 0, 0); same.next(0); same.store(1, page(4, "two"), 1, 1); same.next(1);
same.prune(0, IDLE_TTL + 1);
assert.equal(same.get(1), undefined);
assert.equal(same.store(1, page(4, "two"), 1), false);
assert.equal(same.visits.length, 3);
assert.equal(same.store(1, page(4, "different-next"), 1), true);
assert.equal(same.visits.length, 2);
console.log("  ok: unchanged rereads retain downstream while next-cursor changes discard it");

const running = log(10);
assert.equal(visibleSignature([running]), visibleSignature([{ ...running, updated_at: "later", owner_lease_until: "later", state_seq: 999, duration_ms: 7000 }]));
assert.notEqual(visibleSignature([running]), visibleSignature([{ ...running, outcome: "succeeded", finished_at: "later", duration_ms: 10 }]));
assert.notEqual(visibleSignature([running]), visibleSignature([log(9), running]));
assert.notEqual(visibleSignature([running, log(9)]), visibleSignature([log(9), running]));
assert.notEqual(visibleSignature([running]), visibleSignature([{ ...running, resource_name_snapshot: "renamed" }]));
console.log("  ok: update probes compare ordered durable visible facts, exclude heartbeat/live duration");

const details = new DetailCache();
for (let index = 0; index < 55; index++) details.set(log(index + 1), "1", index);
assert.equal(details.ids().length, 50);
assert.ok(details.ids().includes("1"));
assert.equal(details.get("2", 56), undefined);
details.prune("55", IDLE_TTL + 100);
assert.deepEqual(details.ids(), ["55"]);
assert.ok(details.get("55", 2 * IDLE_TTL, true), "active readers stay pinned even when timers were throttled");
details.clear(); assert.equal(details.ids().length, 0);
console.log("  ok: detail cache caps 50 entries, pins open detail, removes idle entries after 10 minutes");

assert.equal(validEntityID("9223372036854775807"), true);
assert.equal(validDetailID("18446744073709551615"), true);
for (const id of ["18446744073709551616", "0", "-1", "001", "1.2"]) assert.equal(validDetailID(id), false);
for (const invalidID of ["9223372036854775808", "18446744073709551615", "0", "-1", "1.5"]) { assert.equal(validEntityID(invalidID), false); for (const key of ["user_id", "api_key_id", "server_id", "resource_id"]) assert.equal(validFilters({ ...query, [key]: invalidID }), false); }
assert.equal(validFilters(query), true);
for (const from of ["2026-10-01", "2026-10-01T00:00:00", "2026-02-30T00:00:00Z", "2026-10-01T25:00:00Z"]) assert.equal(validFilters({...query, from}), false);
assert.equal(validFilters({...query, from: "2026-10-01T08:00:00.123456789+08:00"}), true);
assert.equal(validFilters({ ...query, request_id: "req:A_1.-" }), true);
assert.equal(validFilters({ ...query, user_id: "9007199254741999" }), true);
for (const invalid of [{ page_size: 101 }, { user_id: "1.5" }, { api_key_id: "0" }, { request_id: "secret with spaces" }, { from: "invalid" }, { to: query.from }, { to: "2027-10-02T00:00:00Z" }, {action: "unexpected"}, {outcome: "unexpected"}, {auth_type: "unexpected"}, {resource_type: "unexpected"}]) assert.equal(validFilters({ ...query, ...invalid }), false);
for (const params of [new URLSearchParams({preset: "custom"}), new URLSearchParams({from: query.from}), new URLSearchParams({preset: "custom", from: "invalid", to: query.to})]) {
  const value = filtersFromURL(params); assert.equal(value.preset, "custom"); assert.equal(validFilters(value.query), false);
}
const allFilters = { ...query, ...Object.fromEntries(filterKeys.map(key => [key, key.endsWith("_id") && key !== "request_id" ? "123" : "value"])) };
const url = filtersToURL(allFilters, "custom", "9007199254741999");
assert.equal(url.has("cursor"), false); assert.equal(url.has("id_upper_bound"), false);
assert.deepEqual(filtersFromURL(url).query, allFilters);
assert.equal(filtersFromURL(url).preset, "custom");
assert.equal(canonicalFilters(allFilters), canonicalFilters(Object.fromEntries(Object.entries(allFilters).reverse())));
assert.equal(Date.parse(resolveRange("7d", 1_000_000_000).to) - Date.parse(resolveRange("7d", 1_000_000_000).from), 7 * 86400_000);
for (const pageSize of ["101", "0", "-25", "abc", "NaN", "Infinity", "25.5", "025", "1e2", ""]) {
  const normalized = filtersFromURL(new URLSearchParams({page_size: pageSize})).query;
  assert.equal(normalized.page_size, 25); assert.equal(validFilters(normalized), true);
}
for (const pageSize of [25, 50, 100]) assert.equal(filtersFromURL(new URLSearchParams({page_size: String(pageSize)})).query.page_size, pageSize);
const customURL = filtersToURL(query, "custom");
assert.deepEqual(filtersFromURL(customURL, 5_000_000_000).query, query);
customURL.delete("preset"); assert.equal(filtersFromURL(customURL).preset, "custom", "explicit URL range without a preset stays explicit");
const relativeURL = filtersToURL(query, "1h");
const relative = filtersFromURL(relativeURL, 5_000_000_000).query;
assert.deepEqual({from: relative.from, to: relative.to}, resolveRange("1h", 5_000_000_000), "new relative URL sessions resolve now rather than replay stale bounds");
const refreshed = refreshFilters({...relative, user_id: "9007199254741999"}, "1h", 5_000_060_000);
assert.equal(Date.parse(refreshed.to) - Date.parse(relative.to), 60_000);
assert.equal(refreshed.user_id, "9007199254741999");
assert.strictEqual(refreshFilters(query, "custom", 5_000_060_000), query);
console.log("  ok: filters validate bounds, canonicalize, round-trip URL without cursor, presets resolve once");
Date.now = realNow;
console.log("\nusage-logs-session: ALL PASS");
