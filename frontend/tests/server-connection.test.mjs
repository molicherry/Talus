import assert from "node:assert/strict";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
import { ConnectionConfigSchema, ConnectionInputSchema, connectionDraft, connectionErrors, canKeepProxyPassword, connectionChanged, serializeConnection } from "./.compiled-connection-config.mjs";

const saved = { mode: "socks5", host: "Proxy.EXAMPLE.test", port: 1080, auth_type: "username_password", username: "proxy-user", has_password: true };
const draft = connectionDraft(saved);
assert.equal(draft.password, "", "never invent a password placeholder");
assert.equal(canKeepProxyPassword(draft, saved), true);
assert.equal(connectionChanged(draft, saved), false);
assert.deepEqual(connectionErrors(draft, saved), {});
assert.equal(Object.hasOwn(serializeConnection(draft), "password"), false, "blank means omission");
for (const change of [{ host: "other.test" }, { port: "1081" }, { username: "other-user" }]) {
  assert.equal(canKeepProxyPassword({ ...draft, ...change }, saved), false);
  assert.equal(connectionErrors({ ...draft, ...change }, saved).password, "passwordRequired");
}
assert.equal(canKeepProxyPassword({ ...draft, host: "proxy.example.test." }, saved), true);
assert.equal(connectionErrors({ ...draft, password: "🙂".repeat(64) }, saved).password, "passwordRequired", "UTF-8 length, not character count");
assert.equal(connectionErrors({ ...draft, username: "中".repeat(86) }, saved).username, "usernameLength");
assert.equal(connectionChanged({ ...draft, password: "new-secret" }, saved), true);
assert.equal(serializeConnection({ ...draft, password: "new-secret" }).password, "new-secret");
assert.deepEqual(serializeConnection({ ...draft, authType: "none", password: "stale", username: "stale" }), { mode: "socks5", host: "proxy.example.test", port: 1080, auth_type: "none" });
assert.deepEqual(serializeConnection({ ...draft, mode: "direct" }), { mode: "direct" });
assert.deepEqual(serializeConnection({ ...draft, mode: "ssh_jump", jumpServerID: "3" }), { mode: "ssh_jump", jump_server_id: 3 });
assert.equal(connectionErrors({ ...draft, mode: "ssh_jump", jumpServerID: "" }).jumpServerID, "jumpRequired");
for (const host of ["https://proxy.test", "user:password@proxy.test", "proxy.test/path", "proxy test", ""]) assert.equal(connectionErrors({ ...draft, host }).host, "hostInvalid");
for (const port of ["0", "65536", "1.5", "no"]) assert.equal(connectionErrors({ ...draft, port }).port, "portInvalid");
assert.deepEqual(connectionErrors({ ...draft, host: "[2001:db8::1]", password: "secret" }, saved), {});
for (const data of [{ mode: "direct" }, { mode: "ssh_jump", jump_server_id: 3 }, { mode: "socks5", host: "proxy.test", port: 1080, auth_type: "none" }, { ...serializeConnection(draft), password: "new-secret" }]) assert.equal(ConnectionInputSchema.safeParse(data).success, true);
assert.equal(ConnectionInputSchema.safeParse({ mode: "direct", password: "stale" }).success, false);
assert.equal(ConnectionInputSchema.safeParse({ mode: "ssh_jump" }).success, false, "summary is not a write DTO");
assert.equal(ConnectionInputSchema.safeParse({ ...serializeConnection(draft), password: "" }).success, false);
assert.deepEqual(ConnectionConfigSchema.parse({ ...saved, password: "must-not-enter-state", encrypted_password: "hidden", salt: "hidden" }), saved, "response schema strips unexpected secret fields");
assert.deepEqual(ConnectionConfigSchema.parse({ mode: "ssh_jump" }), { mode: "ssh_jump" }, "API key summary does not require private detail fields");
console.log("SSH connection drafts, secret preservation, mode serialization and read/write schemas passed.");

nameSuite("server-connection", import.meta.url);
recordCase("SSH connection drafts preserve proxy secrets and serialize route modes", true);
writeReport();
