import assert from "node:assert/strict";

const values = new Map();
const events = new Map();
globalThis.window = {
  localStorage: {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  },
  addEventListener: (name, fn) => events.set(name, fn),
};
const auth = await import("./.compiled-auth.mjs");
const query = await import("./.compiled-query.mjs");
const token = (uid, expires = Date.now() + 60_000, role = "admin") =>
  `header.${Buffer.from(JSON.stringify({ uid, username: `用户${uid}`, role, exp: expires / 1_000 })).toString("base64url")}.signature`;
let notifications = 0;
const stop = auth.subscribeAuth(() => notifications++);

const first = token(1);
auth.setAuthToken(first);
assert.equal(auth.getAuthSnapshot().user.username, "用户1");
assert.equal(auth.getAuthSnapshot().canViewUsageLogs, true);
const epochA = auth.getAuthEpoch();
const entry = query.getEntry('["usage-log",1,"1"]');
entry.queryKey = ["usage-log", 1, "1"];
await query.runQuery(entry, async () => "account-a-private");

auth.setAuthToken(token(2));
assert.notEqual(auth.getAuthEpoch(), epochA);
assert.equal(entry.data, undefined);
assert.equal(auth.clearAuthToken(epochA), false);
assert.equal(auth.denyUsageLogs(epochA), false);
assert.equal(auth.getAuthSnapshot().user.id, 2);
const epochB = auth.getAuthEpoch();
const revisionB = auth.getAuthSnapshot().usagePermissionRevision;
const serverEntry = query.getEntry('["servers"]');
serverEntry.queryKey = ["servers"];
await query.runQuery(serverEntry, async () => "same-account-servers");
const logEntries = ["usage-logs", "usage-logs-probe", "usage-log"].map(prefix => {
  const result = query.getEntry(JSON.stringify([prefix, epochB])); result.queryKey = [prefix, epochB]; return result;
});
for (const result of logEntries) await query.runQuery(result, async () => "private-logs");
let releaseServer, serverSignal;
const serverWork = query.runQuery(serverEntry, ({signal}) => { serverSignal = signal; return new Promise(resolve => { releaseServer = resolve; }); });
let releaseLate;
const late = query.runQuery(logEntries[0], () => new Promise(resolve => { releaseLate = resolve; }));
await Promise.resolve();
assert.equal(auth.denyUsageLogs(epochB), true);
assert.equal(auth.getAuthSnapshot().canViewUsageLogs, false);
assert.equal(auth.getAuthSnapshot().user.id, 2);
assert.equal(auth.getAuthEpoch(), epochB, "permission denial is not an identity change");
assert.equal(auth.getAuthSnapshot().usagePermissionRevision, revisionB + 1);
assert.equal(serverEntry.data, "same-account-servers", "unrelated cached resources survive a log denial");
assert.strictEqual(query.getEntry(serverEntry.key), serverEntry);
assert.equal(serverSignal.aborted, false, "log denial does not cancel another feature's request");
releaseServer("fresh-servers"); await serverWork;
assert.equal(serverEntry.data, "fresh-servers");
for (const result of logEntries) assert.equal(result.data, undefined);
releaseLate("late-private-logs"); await late;
assert.equal(logEntries[0].data, undefined, "late logs cannot repopulate revoked entries");
assert.equal(auth.denyUsageLogs(epochB), true);
assert.equal(auth.getAuthSnapshot().usagePermissionRevision, revisionB + 1, "repeat denial is idempotent");
assert.equal(auth.clearAuthToken(epochB), true, "an in-flight session failure still applies after log denial");
assert.equal(serverEntry.data, undefined, "a real logout still removes every cache");

// Other-tab login and logout synchronously invalidate the local session.
values.set("auth_token", token(3));
events.get("storage")({ key: "auth_token" });
assert.equal(auth.getAuthSnapshot().user.id, 3);
values.delete("auth_token");
events.get("storage")({ key: "auth_token" });
assert.equal(auth.getAuthSnapshot().user, null);

// Expiry updates subscribers even when no React component renders or requests.
auth.setAuthToken(token(4, Date.now() + 35));
const expiryEpoch = auth.getAuthEpoch();
await new Promise((resolve) => setTimeout(resolve, 65));
assert.equal(auth.getAuthSnapshot().user, null);
assert.ok(auth.getAuthEpoch() > expiryEpoch);
assert.equal(values.has("auth_token"), false);
const loggedOutEpoch = auth.getAuthEpoch();
auth.clearAuthToken();
assert.equal(auth.getAuthEpoch(), loggedOutEpoch, "already-logged-out 401 must not repeatedly invalidate caches");

// Malformed and already-expired payloads never establish a visible session.
auth.setAuthToken("invalid-token");
assert.equal(auth.getAuthToken(), null);
auth.setAuthToken(token(5, Date.now() - 1));
assert.equal(auth.getAuthSnapshot().user, null);
assert.ok(notifications >= 7);
stop();
auth.clearAuthToken();
console.log("auth-session: ALL PASS");
