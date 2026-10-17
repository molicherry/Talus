import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const values = new Map();
const redirects = [];
globalThis.window = {
  localStorage: {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  },
  addEventListener: () => {},
  location: { pathname: "/servers", replace: (path) => redirects.push(path) },
};
const auth = await import("./.compiled-auth.mjs");
await import("./.compiled-query.mjs");
const source = await readFile(new URL("../src/lib/api-client.ts", import.meta.url), "utf8");
let compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText;
// Only browser-build imports are stubbed; requests run the actual API client.
const translated = "data:text/javascript," + encodeURIComponent("export default {t:()=> 'API error'};");
compiled = compiled.replace(/"\.\.\/i18n"/g, JSON.stringify(translated))
  .replace(/"\.\/auth"/g, JSON.stringify(new URL("./.compiled-auth.mjs", import.meta.url).href))
  .replace(/"\.\/unauthorized"/g, JSON.stringify(new URL("./.compiled-unauthorized.mjs", import.meta.url).href))
  .replace("import.meta.env.VITE_API_BASE_URL", '""');
const { apiClient } = await import("data:text/javascript;base64," + Buffer.from(compiled).toString("base64"));
const token = (uid) => `header.${Buffer.from(JSON.stringify({uid, username: `user${uid}`, role: "admin", exp: Date.now()/1_000 + 60})).toString("base64url")}.signature`;
const pending = [];
globalThis.fetch = (_url, options) => new Promise((resolve) => pending.push({resolve, options}));
const reply = (index, status, reason) => pending[index].resolve(new Response(JSON.stringify({ error: {code: status, reason} }), {status}));

for (const status of [401, 403]) {
  auth.setAuthToken(token(1));
  const oldEpoch = auth.getAuthEpoch();
  const request = apiClient.get("/api/v1/usage-logs");
  const index = pending.length - 1;
  auth.setAuthToken(token(2));
  const currentToken = auth.getAuthToken();
  assert.notEqual(auth.getAuthEpoch(), oldEpoch);
  reply(index, status, status === 401 ? "unauthorized" : "forbidden");
  await assert.rejects(request, (error) => error.status === status);
  assert.equal(auth.getAuthToken(), currentToken);
  assert.equal(auth.getAuthSnapshot().canViewUsageLogs, true);
  assert.equal(redirects.length, 0);
}

// An earlier unrelated 401 must still log out after a log-only 403.
{
  const currentToken = auth.getAuthToken();
  const epoch = auth.getAuthEpoch();
  const serverRequest = apiClient.get("/api/v1/servers");
  const serverIndex = pending.length - 1;
  const lateLogs = apiClient.get("/api/v1/usage-logs?from=frozen");
  const lateIndex = pending.length - 1;
  const request = apiClient.get("/api/v1/usage-logs/123");
  reply(pending.length - 1, 403, "forbidden");
  await assert.rejects(request);
  assert.equal(auth.getAuthToken(), currentToken);
  assert.equal(auth.getAuthSnapshot().canViewUsageLogs, false);
  assert.equal(auth.getAuthEpoch(), epoch);
  pending[lateIndex].resolve(new Response(JSON.stringify({data: "late-private-log"}), {status: 200}));
  await assert.rejects(lateLogs, error => error.name === "AbortError");
  assert.equal(auth.getAuthSnapshot().canViewUsageLogs, false);
  reply(serverIndex, 401, "unauthorized");
  await assert.rejects(serverRequest);
  assert.equal(auth.getAuthToken(), null);
  assert.deepEqual(redirects.splice(0), ["/login"]);
}

// Other 403 reasons (including a proxy without an envelope reason) stay inline.
for (const reason of [undefined, "server_credentials_unavailable"]) {
  auth.setAuthToken(token(3));
  const epoch = auth.getAuthEpoch();
  const request = apiClient.get("/api/v1/usage-logs/filter-options?kind=server");
  reply(pending.length - 1, 403, reason);
  await assert.rejects(request);
  assert.equal(auth.getAuthEpoch(), epoch);
  assert.equal(auth.getAuthSnapshot().canViewUsageLogs, true);
}

// A generic resource 403 does not revoke login or log permission.
{
  auth.setAuthToken(token(3));
  const epoch = auth.getAuthEpoch();
  const request = apiClient.get("/api/v1/servers/1");
  reply(pending.length - 1, 403, "forbidden");
  await assert.rejects(request);
  assert.equal(auth.getAuthEpoch(), epoch);
  assert.equal(auth.getAuthSnapshot().canViewUsageLogs, true);
}

// Cancellation blocks even a transport that returns a late authorization error.
{
  const controller = new AbortController();
  const epoch = auth.getAuthEpoch();
  const request = apiClient.get("/api/v1/usage-logs", {signal: controller.signal});
  controller.abort();
  reply(pending.length - 1, 401, "unauthorized");
  await assert.rejects(request);
  assert.equal(auth.getAuthEpoch(), epoch);
  assert.equal(redirects.length, 0);
}

// Form credential errors remain inline; current session failures still redirect.
{
  const epoch = auth.getAuthEpoch();
  const formRequest = apiClient.post("/api/v1/auth/change-password", {});
  reply(pending.length - 1, 401, "current_password_incorrect");
  await assert.rejects(formRequest);
  assert.equal(auth.getAuthEpoch(), epoch);
  const sessionRequest = apiClient.get("/api/v1/servers");
  reply(pending.length - 1, 401, "unauthorized");
  await assert.rejects(sessionRequest);
  assert.equal(auth.getAuthToken(), null);
  assert.deepEqual(redirects, ["/login"]);
}
// A proxy 401 on the unauthenticated setup probe cannot cause an epoch/refetch loop.
{
  window.location.pathname = "/login";
  const epoch = auth.getAuthEpoch();
  const request = apiClient.get("/api/v1/auth/setup");
  reply(pending.length - 1, 401, "unauthorized");
  await assert.rejects(request);
  assert.equal(auth.getAuthEpoch(), epoch);
  assert.deepEqual(redirects, ["/login"]);
}

// Binary transfers cannot publish bytes after logout or an identity switch.
{
  const calls = [];
  class MockXHR {
    upload = {};
    headers = {};
    open(method, url) { this.method = method; this.url = url; calls.push(this); }
    setRequestHeader(key, value) { this.headers[key] = value; }
    send(body) { this.body = body; }
    abort() { this.aborted = true; this.onabort?.(); }
    async reply(status, body) { this.status = status; this.response = body; await this.onload?.(); }
  }
  globalThis.XMLHttpRequest = MockXHR;
  auth.setAuthToken(token(11));
  const controller = new AbortController();
  const input = new Blob([new Uint8Array([0, 255, 1])]);
  const pendingFile = apiClient.transfer("/api/v1/servers/1/sftp/upload", { method: "POST", body: input, signal: controller.signal });
  const xhr = calls.at(-1);
  assert.equal(xhr.headers.Authorization, `Bearer ${auth.getAuthToken()}`);
  assert.equal(xhr.body, input);
  controller.abort();
  await assert.rejects(pendingFile, error => error.name === "AbortError");
  const stale = apiClient.transfer("/api/v1/servers/1/sftp/download", { download: true });
  const staleXHR = calls.at(-1);
  auth.setAuthToken(token(12));
  const switchedToken = auth.getAuthToken();
  await assert.rejects(stale, error => error.name === "AbortError");
  assert.equal(staleXHR.aborted, true);
  await staleXHR.reply(200, input);
  assert.equal(auth.getAuthToken(), switchedToken);
  const download = apiClient.transfer("/api/v1/servers/1/sftp/download", { download: true });
  await calls.at(-1).reply(200, input);
  assert.deepEqual(new Uint8Array(await (await download).arrayBuffer()), new Uint8Array([0, 255, 1]));
  const failed = apiClient.transfer("/api/v1/servers/1/sftp/upload", { method: "POST", body: input });
  await calls.at(-1).reply(409, new Blob([JSON.stringify({error:{code:409,reason:"sftp_exists"}})]));
  await assert.rejects(failed, error => error.reason === "sftp_exists");
}
auth.clearAuthToken();
console.log("api-client-session: ALL PASS");
