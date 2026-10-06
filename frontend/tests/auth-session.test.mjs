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
assert.equal(auth.denyUsageLogs(epochB), true);
assert.equal(auth.getAuthSnapshot().canViewUsageLogs, false);
assert.equal(auth.getAuthSnapshot().user.id, 2);
assert.notEqual(auth.getAuthEpoch(), epochB);

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
