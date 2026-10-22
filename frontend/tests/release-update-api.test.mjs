import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const moduleURL = source => "data:text/javascript;base64," + Buffer.from(source).toString("base64");
let response;
const calls = [];
globalThis.releaseUpdateAPIMock = { get: async (path, options) => { calls.push({ path, options }); return response; } };
const source = ts.transpileModule(await readFile(new URL("../src/features/updates/api.ts", import.meta.url), "utf8"), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText.replace('"zod"', JSON.stringify(import.meta.resolve("zod")))
  .replace('"../../lib/api-client"', JSON.stringify(moduleURL("export const apiClient = globalThis.releaseUpdateAPIMock;")));
const { getUpdateStatus } = await import(moduleURL(source));
const release = { version: "v0.13.0", published_at: "2026-10-22T12:00:00.123456789Z", notes: "# Highlights", url: "https://github.com/molicherry/Talus/releases/tag/v0.13.0" };
response = { current_version: "v0.12.0", latest_release: release, update_available: true, checked_at: "2026-10-22T12:00:00.123456789Z" };
const signal = new AbortController().signal;
assert.deepEqual(await getUpdateStatus({ signal }), response);
assert.equal(calls.at(-1).path, "/api/v1/updates");
assert.equal(calls.at(-1).options.signal, signal);
assert.equal(calls.at(-1).options.cache, "default", "browser must be allowed to revalidate ETags");
for (const url of ["javascript:alert(1)", "data:text/html,hello", "http://github.com/molicherry/Talus/releases/tag/v0.13.0", "https://github.com.evil.test/molicherry/Talus/releases/tag/v0.13.0", "https://github.com/other/Talus/releases/tag/v0.13.0", release.url + "?redirect=x", release.url + "\n", release.url + "-rc.1"]) {
  response = { ...response, latest_release: { ...release, url } };
  await assert.rejects(getUpdateStatus({ signal }), undefined, `unsafe/noncanonical release URL accepted: ${url}`);
}
response = { current_version: "dev", latest_release: null, update_available: false, checked_at: null };
assert.deepEqual(await getUpdateStatus({ signal }), response);
delete globalThis.releaseUpdateAPIMock;
console.log("Release API timestamps, nullable status, conditional caching and canonical HTTPS URLs passed.");
