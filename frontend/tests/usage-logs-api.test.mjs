import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const compile = source => ts.transpileModule(source, {
  compilerOptions: {target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext},
}).outputText;
const moduleURL = source => "data:text/javascript;base64," + Buffer.from(source).toString("base64");
const typesURL = moduleURL(compile(await readFile(new URL("../src/features/usage-logs/types.ts", import.meta.url), "utf8")));
const calls = [];
let response;
globalThis.usageAPIMock = {get: async path => {calls.push(path); return response;}};
const source = compile(await readFile(new URL("../src/features/usage-logs/api.ts", import.meta.url), "utf8"))
  .replace('"zod"', JSON.stringify(import.meta.resolve("zod")))
  .replace('"../../lib/api-client"', JSON.stringify(moduleURL("export const apiClient = globalThis.usageAPIMock;")))
  .replace('"./types"', JSON.stringify(typesURL));
const {getUsageLogs, getUsageLog} = await import(moduleURL(source));
const exact = "9223372036854775807";
const log = {id: "18446744073709551615", operation_id: "op", started_at: "2026-10-01T00:00:00Z", outcome: "failed", phase: "closed", action: "server.exec", resource_type: "server", auth_type: "jwt", source: "operation", resource_id: exact, server_id: exact, user_id: exact, api_key_id: exact, legacy_audit_event_id: exact};
const query = {from: "2026-10-01T00:00:00Z", to: "2026-10-02T00:00:00Z", page_size: 25, user_id: exact, resource_id: exact, server_id: exact, api_key_id: exact};
response = {items: [log], query, id_upper_bound: log.id, next_cursor: "opaque-cursor", has_more: true, consistency: "bounded_keyset"};
const page = await getUsageLogs(query, "opaque-cursor");
assert.deepEqual(page.items[0], log);
const params = new URL(calls.at(-1), "http://example.test").searchParams;
for (const field of ["resource_id", "server_id", "user_id", "api_key_id"]) assert.equal(params.get(field), exact);
assert.equal(params.get("cursor"), "opaque-cursor");
response = log;
assert.deepEqual(await getUsageLog(log.id), log);
assert.equal(calls.at(-1), `/api/v1/usage-logs/${log.id}`);

// IDs from JSON numbers must fail visibly instead of silently rounding before
// the user copies an identifier, navigates, or filters by it.
for (const field of ["id", "resource_id", "server_id", "user_id", "api_key_id", "legacy_audit_event_id"]) {
  response = {...log, [field]: Number(exact)};
  await assert.rejects(getUsageLog(log.id));
}
response = { ...log, resource_id: undefined, server_id: undefined, user_id: null, api_key_id: null, legacy_audit_event_id: undefined };
assert.equal((await getUsageLog(log.id)).user_id, null);
delete globalThis.usageAPIMock;
console.log("usage-logs-api: ALL PASS");
