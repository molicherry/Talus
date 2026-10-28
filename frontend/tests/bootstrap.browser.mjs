// Built SPA bootstrap UX against bounded dummy APIs. Real Hub ownership and
// transaction enforcement are covered by Go/startup/e2e integration tests.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright");
const root = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.UI_TEST_PORT ?? 4183);
const base = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, ["node_modules/vite/bin/vite.js", "preview",
  "--host", "127.0.0.1", "--port", String(port), "--strictPort"],
{ cwd: root, stdio: "pipe" });
let output = "";
for (const pipe of [server.stdout, server.stderr]) pipe.on("data", (data) => {
  output = (output + data).slice(-65536);
});
const proof = "owned-browser-bootstrap-proof-0123456789";
const jwt = `test.${Buffer.from(JSON.stringify({ uid: 1, username: "owner", role: "admin", exp: Date.now() / 1000 + 3600 })).toString("base64url")}.test`;
async function until(check, description) {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(20);
  }
  throw new Error(`Timed out: ${description}`);
}
async function fixture(browser, initialized = false, bootstrapEnabled = true) {
  const context = await browser.newContext({ locale: "en-US" });
  await context.addInitScript(() => localStorage.setItem("i18nextLng", "en"));
  const state = { initialized, attempts: [], errors: [] };
  await context.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    const reply = (data, status = 200) => route.fulfill({ status,
      contentType: "application/json", body: JSON.stringify(status >= 400 ? data : { data }) });
    if (path === "/api/v1/auth/setup" && method === "GET") return reply({ needed: !state.initialized, bootstrap_enabled: bootstrapEnabled });
    if (path === "/api/v1/auth/setup" && method === "POST") {
      const body = request.postDataJSON();
      state.attempts.push({ path, body });
      if (body.token !== proof) return reply({ error: { reason: "forbidden", message: "forbidden" } }, 403);
      if (state.initialized) return reply({ error: { reason: "conflict", message: "conflict" } }, 409);
      state.initialized = true;
      return reply({ token: jwt });
    }
    if (path === "/api/v1/auth/login" && method === "POST") {
      state.attempts.push({ path, body: request.postDataJSON() });
      return reply({ error: { reason: "invalid_credentials", message: "invalid credentials" } }, 401);
    }
    if (path === "/api/v1/version") return reply({ version: "dev" });
    if (path === "/api/v1/updates") return reply({ current_version: "dev", latest_release: null, update_available: false, checked_at: null });
    return reply([]);
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => state.errors.push(error.message));
  return { context, page, state };
}
nameSuite("bootstrap", import.meta.url);
let browser;
try {
  await until(async () => {
    if (server.exitCode !== null) throw new Error(`Vite exited: ${output}`);
    return fetch(base).then((response) => response.ok).catch(() => false);
  }, "the built SPA preview");
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE || undefined,
    args: JSON.parse(process.env.CHROMIUM_ARGS ?? "[]") });
  {
    const { context, page, state } = await fixture(browser, false, false);
    const name = "disabled bootstrap explains deployment configuration without offering a form";
    try {
      await page.goto(`${base}/login`);
      await page.waitForURL("**/setup");
      await page.getByRole("alert").getByText("Administrator setup is disabled", { exact: true }).waitFor();
      assert.match(await page.getByRole("alert").innerText(), /BOOTSTRAP_TOKEN/);
      assert.equal(await page.locator("form").count(), 0);
      assert.equal(await page.getByRole("button", { name: "Create Admin Account", exact: true }).count(), 0);
      assert.equal(await page.getByLabel("Setup token", { exact: true }).count(), 0);
      assert.equal(state.attempts.length, 0);
      assert.deepEqual(state.errors, []);
      recordCase(name);
      console.log(`ok: ${name}`);
    } finally { await context.close(); }
  }
  {
    const { context, page, state } = await fixture(browser);
    const name = "fresh setup requires the deployment proof and keeps it out of browser storage";
    try {
      await page.goto(`${base}/login`);
      await page.waitForURL("**/setup");
      await page.getByLabel("Username", { exact: true }).fill("owner");
      await page.getByLabel("Password", { exact: true }).fill("dummy-password");
      await page.getByLabel("Confirm Password", { exact: true }).fill("dummy-password");
      const submit = page.getByRole("button", { name: "Create Admin Account", exact: true });
      await submit.click();
      await page.getByText("Enter the setup token (32–256 bytes).", { exact: true }).waitFor();
      assert.equal(state.attempts.length, 0, "missing proof must not submit the form");
      await page.getByLabel("Setup token", { exact: true }).fill("incorrect-browser-proof-0123456789");
      await submit.click();
      await until(() => state.attempts.length === 1, "the rejected setup attempt");
      await until(async () => !(await submit.isDisabled()), "the failed attempt to finish");
      assert.equal(new URL(page.url()).pathname, "/setup");
      assert.equal(await page.evaluate(() => localStorage.getItem("auth_token")), null);
      await page.getByLabel("Setup token", { exact: true }).fill(proof);
      await submit.click();
      await page.waitForURL((url) => url.pathname === "/");
      assert.equal(state.attempts.length, 2);
      assert.ok(state.attempts.every((attempt) => attempt.path === "/api/v1/auth/setup"), "setup must not use ordinary login");
      assert.deepEqual(state.attempts[1].body, { token: proof, username: "owner", password: "dummy-password" });
      const storage = await page.evaluate(() => ({ token: localStorage.getItem("auth_token"),
        persisted: JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }) }));
      assert.equal(storage.token, jwt);
      assert.ok(!storage.persisted.includes(proof), "only the session, never the ownership proof, is persisted");
      assert.ok(!page.url().includes(proof));
      assert.deepEqual(state.errors, []);
      recordCase(name);
      console.log(`ok: ${name}`);
    } finally { await context.close(); }
  }
  {
    const { context, page, state } = await fixture(browser, true, false);
    const name = "initialized instances redirect setup to ordinary login";
    try {
      await page.goto(`${base}/setup`);
      await page.waitForURL("**/login");
      await page.getByRole("button", { name: "Sign in", exact: true }).waitFor();
      assert.equal(await page.getByLabel("Setup token", { exact: true }).count(), 0);
      assert.equal(state.attempts.length, 0);
      assert.deepEqual(state.errors, []);
      recordCase(name);
      console.log(`ok: ${name}`);
    } finally { await context.close(); }
  }
} catch (error) {
  recordCase("bootstrap browser suite", false);
  console.error(error.stack);
  process.exitCode = 1;
} finally {
  await browser?.close();
  server.kill("SIGTERM");
  writeReport();
}
