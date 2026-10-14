// Optional browser regression suite. Build first; all API traffic is mocked.
// See tests/README.md for browser setup and environment overrides.
import assert from "node:assert/strict";

import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright");
const root = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.UI_TEST_PORT ?? 4177);

const base = `http://127.0.0.1:${port}`;
const server = spawn(
  process.execPath,
  [
    "node_modules/vite/bin/vite.js",
    "preview",
    "--host",
    "127.0.0.1",
    "--port",
    String(port),
    "--strictPort",
  ],
  { cwd: root, stdio: "pipe" },
);
let serverOutput = "";
server.stdout.on("data", (data) => {
  serverOutput += data;
});
server.stderr.on("data", (data) => {
  serverOutput += data;
});

async function until(check, description = "condition", timeout = 5000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(20);
  }
  throw new Error(`Timed out: ${description}`);
}
function gate() {
  let release;
  const promise = new Promise((resolve) => {
    release = resolve;
  });
  return { promise, release };
}
const valueIs = (locator, expected) =>
  until(async () => (await locator.inputValue()) === expected, `input value ${expected}`);
async function capture(page, name) {
  if (!process.env.UI_TEST_SCREENSHOTS) return;
  await mkdir(process.env.UI_TEST_SCREENSHOTS, { recursive: true });
  await page.screenshot({
    path: join(process.env.UI_TEST_SCREENSHOTS, `${name}.png`),
    fullPage: true,
  });
}
const timestamp = "2026-01-01T00:00:00Z";
const serverFixture = (id) => ({
  id,
  name: id === 1 ? "Alpha" : "Beta",
  host: `server-${id}.example.test`,
  port: 22,
  owner_id: 1,
  status: "online",
  created_at: timestamp,
  credential_id: 1,
  latest_metrics: { cpu_percent: 0, memory_percent: 25, disk_percent: 40 },
});
const serviceFixture = (id) => ({
  id,
  name: `service-${id}`,
  display_name: `Service ${id}`,
  base_url: "https://example.test",
  description: "original",
  created_at: timestamp,
  credential_hints: { token: "first hint", token2: "second hint" },
});

async function fixture(browser, mobile = false) {
  const context = await browser.newContext({
    viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 900 },
    hasTouch: mobile,
    isMobile: mobile,
    locale: "en-US",
  });
  await context.addInitScript(() => {
    localStorage.setItem(
      "auth_token",
      `test.${btoa(JSON.stringify({ uid: 1, username: "review-user", role: "admin", exp: Date.now() / 1000 + 3600 }))}.test`,
    );
    localStorage.setItem("i18nextLng", "en");
    localStorage.setItem("talus-theme", "light");
  });
  const state = {
    servers: [serverFixture(1), serverFixture(2)],
    services: [serviceFixture(1), serviceFixture(2)],
    credentials: [
      {
        id: 1,
        name: "SSH example",
        auth_type: "password",
        username: "root",
        created_at: timestamp,
      },
    ],
    failReads: new Set(),
    writes: [],
    counts: new Map(),
    custom: null,
    errors: [],
  };
  await context.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();
    const body = method === "PUT" || method === "POST" ? request.postDataJSON() : undefined;
    const key = `${method} ${path}`;
    state.counts.set(key, (state.counts.get(key) ?? 0) + 1);
    if (method !== "GET") state.writes.push({ path, method, body });
    const reply = (data, status = 200) =>
      route
        .fulfill({
          status,
          contentType: "application/json",
          body: status === 204 ? "" : JSON.stringify(status >= 400 ? data : { data }),
        })
        .catch(() => {});
    if (state.custom && (await state.custom({ path, method, body, reply, url }))) return;
    if (method === "GET" && state.failReads.has(path))
      return reply({ error: { reason: "internal_error", code: 503 } }, 503);
    if (path === "/api/v1/servers/summary" || path === "/api/v1/servers")
      return reply(state.servers);
    if (/\/servers\/\d+\/metrics$/.test(path)) {
      const stat = url.searchParams.get("stat") ?? "avg";
      const fromMs = Date.parse(url.searchParams.get("from"));
      const toMs = Date.parse(url.searchParams.get("to"));
      const requestedRaw = url.searchParams.get("interval") ?? "auto";
      // The backend resolves "auto" to the finest granularity the budget allows;
      // the mock mirrors that so the chart aligns to the same grid.
      const effective =
        requestedRaw === "auto"
          ? toMs - fromMs <= 3600000
            ? "1m"
            : toMs - fromMs <= 21600000
              ? "5m"
              : toMs - fromMs <= 86400000
                ? "15m"
                : toMs - fromMs <= 604800000
                  ? "1h"
                  : "6h"
          : requestedRaw;
      const requested = requestedRaw;
      const interval =
        { "1m": 60000, "5m": 300000, "15m": 900000, "1h": 3600000, "6h": 21600000 }[effective] ?? 60000;
      const first = Math.floor(fromMs / interval) * interval;
      const last = toMs;
      const points = [];
      for (let time = first, index = 0; time <= last; time += interval, index++) {
        if (index === 1) continue; // A gap must remain distinct from real zero.
        points.push({
          time: new Date(time).toISOString(),
          // The newest sample inside the bucket, not the bucket start.
          sampled_at: new Date(Math.min(time + interval - 1000, toMs)).toISOString(),
          cpu_percent: index === 0 ? 0 : 30,
          memory_percent: 25,
          disk_percent: 40,
          load_1: 1,
          load_5: 1,
          load_15: 1,
          swap_percent: 0,
          net_recv_rate: 1000,
          net_sent_rate: 2000,
          disk_read_rate: 3000,
          disk_write_rate: 4000,
          cpu_peak: index === 0 ? 0 : 60,
          memory_peak: 50,
          disk_peak: 80,
          swap_peak: 0,
          load_1_peak: 2,
          load_5_peak: 2,
          load_15_peak: 2,
          net_recv_peak: 5000,
          net_sent_peak: 6000,
          disk_read_peak: 7000,
          disk_write_peak: 8000,
          net_recv_coverage: 60,
          net_sent_coverage: 60,
          disk_read_coverage: 60,
          disk_write_coverage: 60,
          // Per-bucket statistics: the window average is sum/count, never the mean
          // of bucket means (F06).
          cpu_sum: index === 0 ? 0 : 30,
          cpu_count: 1,
          memory_percent_sum: 25,
          memory_percent_count: 1,
          disk_percent_sum: 40,
          disk_percent_count: 1,
          swap_percent_sum: 0,
          swap_percent_count: 1,
          load1_sum: 1,
          load1_count: 1,
          load5_sum: 1,
          load5_count: 1,
          load15_sum: 1,
          load15_count: 1,
        });
      }
      // The endpoint answers with the §4.2.1 envelope, never a bare array.
      return reply({
        server_id: 1,
        from: url.searchParams.get("from"),
        to: url.searchParams.get("to"),
        requested_interval: requested,
        actual_interval: effective,
        coarsen_reason: "",
        stat,
        coverage: 0.75,
        source: effective === "6h" ? "rollup" : "raw",
        downsampled: effective === "6h",
        points,
      });
    }
    if (/\/servers\/\d+$/.test(path)) {
      if (method === "DELETE") {
        state.servers = state.servers.filter((s) => s.id !== Number(path.split("/").at(-1)));
        return reply(null, 204);
      }
      return reply(state.servers.find((s) => s.id === Number(path.split("/").at(-1))));
    }
    if (path === "/api/v1/services") return reply(state.services);
    if (/\/services\/\d+\/credentials$/.test(path))
      return reply({ token: "first-example", token2: "second-example" });
    if (/\/services\/\d+$/.test(path)) {
      const id = Number(path.split("/").at(-1));
      if (method === "PUT")
        state.services = state.services.map((s) => (s.id === id ? { ...s, ...body } : s));
      return reply(state.services.find((s) => s.id === id));
    }
    if (path === "/api/v1/credentials") return reply(state.credentials);
    if (/\/credentials\/\d+\/reveal$/.test(path)) return reply({ password: "old-example" });
    if (/\/credentials\/\d+$/.test(path)) return reply({ ...state.credentials[0], ...body });
    if (path === "/api/v1/auth/password") return reply(null, 204);
    if (path === "/api/v1/api-keys") return reply([]);
    return reply({ version: "dev" });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  page.on("pageerror", (error) => state.errors.push(error.message));
  return { page, context, state, goto: (path) => page.goto(`${base}${path}`) };
}

let browser;
let passed = 0;
let failed = 0;
async function test(name, run, mobile = false) {
  if (process.env.UI_TEST_FILTER && !new RegExp(process.env.UI_TEST_FILTER).test(name)) return;
  recordCase(name);
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_EXECUTABLE || undefined,
    args: JSON.parse(process.env.CHROMIUM_ARGS ?? "[]"),
  });
  const f = await fixture(browser, mobile);
  try {
    await run(f);
    assert.deepEqual(f.state.errors, [], "No uncaught page errors");
    passed++;
    console.log(`PASS ${name}`);
  } catch (error) {
    failed++;
    console.error(`FAIL ${name}: ${error.stack}`);
  } finally {
    await f.context.close();
    await browser.close();
    browser = undefined;
  }
}

try {
  await until(async () => {
    if (server.exitCode !== null) throw new Error(serverOutput);
    return fetch(base)
      .then((r) => r.ok)
      .catch(() => false);
  }, "preview server");
  await test("late service secrets preserve edits; duplicate keys never overwrite rows", async ({
    page,
    state,
    goto,
  }) => {
    const pending = gate();
    state.custom = async ({ path, reply }) => {
      if (path.endsWith("/services/1/credentials")) {
        await pending.promise;
        await reply({ token: "first-example", token2: "second-example" });
        return true;
      }
    };
    await goto("/services/1/edit");
    await page.getByLabel("Name", { exact: true }).fill("renamed-service");
    await page.getByLabel("Description", { exact: true }).fill("typed while loading");
    assert.equal(
      await page.getByRole("button", { name: "Update Service", exact: true }).isDisabled(),
      true,
    );
    pending.release();
    await valueIs(page.getByLabel("Value 2", { exact: true }), "second-example");
    assert.equal(await page.getByLabel("Name", { exact: true }).inputValue(), "renamed-service");
    await capture(page, "service-editor");
    await page.getByLabel("Key 2", { exact: true }).fill("token");
    await valueIs(page.getByLabel("Value 1", { exact: true }), "first-example");
    await valueIs(page.getByLabel("Value 2", { exact: true }), "second-example");
    await page.getByRole("button", { name: "Update Service", exact: true }).click();
    await page.getByText("Key names must be unique.", { exact: true }).waitFor();
    assert.equal(state.writes.length, 0);
    await page.getByLabel("Key 2", { exact: true }).fill("renamed");
    await page.getByRole("button", { name: "Update Service", exact: true }).click();
    await until(() => state.writes.length === 1, "service update");
    assert.deepEqual(state.writes[0].body.credentials, {
      token: "first-example",
      renamed: "second-example",
    });
    assert.deepEqual(state.writes[0].body.credential_hints, {
      token: "first hint",
      renamed: "second hint",
    });
    assert.equal(state.writes[0].body.description, "typed while loading");
  });

  await test("new credential rows remain independent and visibility follows row identity", async ({
    page,
    goto,
  }) => {
    await goto("/services/new");
    await page.getByRole("button", { name: "Add Key", exact: true }).click();
    await page.getByRole("button", { name: "Add Key", exact: true }).click();
    assert.notEqual(
      await page.getByLabel("Key 1", { exact: true }).getAttribute("id"),
      await page.getByLabel("Key 2", { exact: true }).getAttribute("id"),
    );
    await page.getByLabel("Value 1", { exact: true }).fill("one");
    await page.getByLabel("Value 2", { exact: true }).fill("two");
    await page.getByRole("button", { name: "Show", exact: true }).first().click();
    await page.getByRole("button", { name: "Remove", exact: true }).first().click();
    assert.equal(await page.getByLabel("Value 1", { exact: true }).inputValue(), "two");
    assert.equal(
      await page.getByLabel("Value 1", { exact: true }).getAttribute("type"),
      "password",
    );
  });

  await test("service secret failure blocks saving and retry retains metadata", async ({
    page,
    state,
    goto,
  }) => {
    state.failReads.add("/api/v1/services/1/credentials");
    await goto("/services/1/edit");
    await page.getByLabel("Name", { exact: true }).fill("kept-on-retry");
    await page.getByText(/Could not load saved credentials/).waitFor();
    assert.equal(
      await page.getByRole("button", { name: "Update Service", exact: true }).isDisabled(),
      true,
    );
    assert.equal(
      await page.getByText("Either password or private key is required", { exact: true }).count(),
      0,
    );
    state.failReads.clear();
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await valueIs(page.getByLabel("Value 1", { exact: true }), "first-example");
    assert.equal(await page.getByLabel("Name", { exact: true }).inputValue(), "kept-on-retry");
  });

  await test("SSH reveal failure/retry preserves username and saves the new password", async ({
    page,
    state,
    goto,
  }) => {
    state.failReads.add("/api/v1/credentials/1/reveal");
    await goto("/credentials/1/edit");
    await page.getByText(/Could not load saved credentials/).waitFor();
    await page.getByLabel("Username", { exact: true }).fill("changed-user");
    assert.equal(await page.locator('button[type="submit"]').isDisabled(), true);
    state.failReads.clear();
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await valueIs(page.locator("#password"), "old-example");
    assert.equal(await page.getByLabel("Username", { exact: true }).inputValue(), "changed-user");
    await page.locator("#password").fill("new-example");
    await page.locator('button[type="submit"]').click();
    await until(() => state.writes.length === 1, "credential update");
    assert.deepEqual(state.writes[0].body, { username: "changed-user", password: "new-example" });
  });

  await test("a response from an abandoned service editor cannot fill the next editor", async ({
    page,
    state,
    goto,
  }) => {
    const pending = gate();
    state.custom = async ({ path, reply }) => {
      if (path.endsWith("/services/1/credentials")) {
        await pending.promise;
        await reply({ obsolete: "old" });
        return true;
      }
    };
    await goto("/services/1/edit");
    await page.getByLabel("Name", { exact: true }).fill("abandoned");
    await page.getByRole("link", { name: "Services", exact: true }).click();
    await page.getByRole("button", { name: "Edit service-2", exact: true }).click();
    await valueIs(page.getByLabel("Value 1", { exact: true }), "first-example");
    pending.release();
    await delay(100);
    assert.equal(await page.getByLabel("Name", { exact: true }).inputValue(), "service-2");
    assert.equal(await page.getByLabel("Key 1", { exact: true }).inputValue(), "token");
  });

  await test("failed credential metadata fetch displays a retryable error, not not-found", async ({
    page,
    state,
    goto,
  }) => {
    state.failReads.add("/api/v1/credentials");
    await goto("/credentials/1/edit");
    await page.getByRole("button", { name: "Retry", exact: true }).waitFor();
    assert.equal(await page.getByText("Credential not found", { exact: true }).count(), 0);
    state.failReads.clear();
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await valueIs(page.locator("#username"), "root");
  });

  for (const [route, label, endpoint, text] of [
    ["/", "Dashboard", "/api/v1/servers", "Alpha"],
    ["/servers", "Servers", "/api/v1/servers/summary", "Alpha"],
    ["/services", "Services", "/api/v1/services", "service-1"],
    ["/credentials", "Credentials", "/api/v1/credentials", "SSH example"],
  ]) {
    await test(`${label}: failed refresh preserves loaded content and can recover`, async ({
      page,
      state,
      goto,
    }) => {
      await page.clock.install();
      await goto(route);
      await page.getByText(text, { exact: true }).first().waitFor();
      state.failReads.add(endpoint);
      if (route === "/") await page.clock.fastForward(61000);
      else {
        await page.getByRole("link", { name: "API Keys", exact: true }).click();
        await page.clock.fastForward(61000);
        await page.getByRole("link", { name: label, exact: true }).click();
      }
      await page.getByText(/Refresh failed. Showing previously loaded data/).waitFor();
      assert.equal(await page.getByText(text, { exact: true }).first().isVisible(), true);
      state.failReads.clear();
      await page.getByRole("button", { name: "Retry", exact: true }).click();
      await until(
        async () =>
          (await page.getByText(/Refresh failed. Showing previously loaded data/).count()) === 0,
        "successful refresh",
      );
    });
  }

  await test("cached empty list still shows a refresh failure", async ({ page, state, goto }) => {
    state.services = [];
    await page.clock.install();
    await goto("/services");
    await page.getByText(/No services/).waitFor();
    state.failReads.add("/api/v1/services");
    await page.getByRole("link", { name: "API Keys", exact: true }).click();
    await page.clock.fastForward(61000);
    await page.getByRole("link", { name: "Services", exact: true }).click();
    await page.getByText(/Refresh failed. Showing previously loaded data/).waitFor();
  });

  await test("delete modal restores focus and blocks every dismiss action while pending", async ({
    page,
    state,
    goto,
  }) => {
    const pending = gate();
    state.custom = async ({ method, path, reply }) => {
      if (method === "DELETE" && path === "/api/v1/servers/1") {
        await pending.promise;
        state.servers = state.servers.filter((s) => s.id !== 1);
        await reply(null, 204);
        return true;
      }
    };
    await goto("/servers");
    const trigger = page.getByRole("button", { name: "Delete Alpha", exact: true });
    await trigger.click();
    const dialog = page.getByRole("dialog");
    await until(
      () =>
        dialog
          .getByRole("button", { name: "Cancel", exact: true })
          .evaluate((el) => el === document.activeElement),
      "cancel initially focused",
    );
    await page.keyboard.press("Escape");
    await until(
      () => trigger.evaluate((el) => el === document.activeElement),
      "delete trigger focus restored",
    );
    await trigger.click();
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await until(() => state.writes.length === 1, "delete started");
    assert.equal(
      await dialog.getByRole("button", { name: "Cancel", exact: true }).isDisabled(),
      true,
    );
    assert.equal(
      await dialog.getByRole("button", { name: "Close dialog", exact: true }).isDisabled(),
      true,
    );
    await page.keyboard.press("Escape");
    await page.mouse.click(5, 5);
    assert.equal(await dialog.isVisible(), true);
    assert.equal(state.writes.length, 1);
    pending.release();
    await dialog.waitFor({ state: "detached" });
  });

  await test("password dialog submits once with Enter, keeps failed input and resets on reopen", async ({
    page,
    state,
    goto,
  }) => {
    let pending = gate();
    let reject = true;
    state.custom = async ({ path, method, reply }) => {
      if (path === "/api/v1/auth/password" && method === "PUT") {
        await pending.promise;
        if (reject)
          await reply({ error: { code: 401, reason: "current_password_incorrect" } }, 401);
        else await reply(null, 204);
        return true;
      }
    };
    await goto("/");
    const trigger = page.getByRole("button", { name: "review-user", exact: true });
    const open = async () => {
      await trigger.click();
      await page.getByRole("button", { name: "Change Password", exact: true }).click();
    };
    await open();
    const dialog = page.getByRole("dialog");
    await valueIs(dialog.locator("#current-password"), "");
    assert.equal(
      await dialog.locator("#current-password").evaluate((el) => el === document.activeElement),
      true,
    );
    await dialog.locator("#current-password").fill("old-example");
    await dialog.locator("#new-password").fill("new-example");
    await dialog.locator("#new-password").press("Enter");
    await until(() => state.writes.length === 1, "password update started");
    await page.keyboard.press("Enter");
    await page.keyboard.press("Escape");
    await page.mouse.click(5, 5);
    assert.equal(await dialog.isVisible(), true);
    assert.equal(state.writes.length, 1);
    pending.release();
    await dialog.getByText("Current password is incorrect", { exact: true }).waitFor();
    assert.equal(await dialog.locator("#new-password").inputValue(), "new-example");
    reject = false;
    pending = gate();
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await until(() => state.writes.length === 2, "password retry started");
    pending.release();
    await dialog.waitFor({ state: "detached" });
    await until(
      () => trigger.evaluate((el) => el === document.activeElement),
      "account focus restored",
    );
    await open();
    assert.equal(await dialog.locator("#current-password").inputValue(), "");
    assert.equal(await dialog.locator("#new-password").inputValue(), "");
    await page.keyboard.press("Escape");
  });

  await test(
    "mobile navigation traps/restores focus, hides closed links and survives resizing",
    async ({ page, goto }) => {
      await goto("/servers");
      await page.getByRole("button", { name: "Delete Alpha", exact: true }).waitFor();
      assert.equal(await page.locator("aside, #mobile-navigation").count(), 0);
      const menu = page.getByRole("button", { name: "Open navigation", exact: true });
      await menu.click();
      const drawer = page.getByRole("dialog");
      await capture(page, "mobile-navigation");
      for (let i = 0; i < 8; i++) {
        await page.keyboard.press("Tab");
        assert.equal(await drawer.evaluate((el) => el.contains(document.activeElement)), true);
      }
      assert.equal(await page.evaluate(() => document.body.style.overflow), "hidden");
      await page.keyboard.press("Escape");
      await until(
        () => menu.evaluate((el) => el === document.activeElement),
        "navigation focus restored",
      );
      assert.notEqual(await page.evaluate(() => document.body.style.overflow), "hidden");
      const button = await page
        .getByRole("button", { name: "Delete Alpha", exact: true })
        .boundingBox();
      assert.ok(button.width >= 44 && button.height >= 44);
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        true,
      );
      await capture(page, "mobile-servers");
      await menu.click();
      await page.setViewportSize({ width: 1280, height: 900 });
      await drawer.waitFor({ state: "detached" });
      assert.notEqual(await page.evaluate(() => document.body.style.overflow), "hidden");
      assert.equal(await page.locator("aside nav").count(), 1);
    },
    true,
  );

  await test(
    "monitoring exposes the range/axis/statistic vocabulary with a data table",
    async ({ page, goto }) => {
      await goto("/servers/1");

      // Ranges: 15m and 30d are offered; 30d asks for the 6h granularity and is
      // shown as downsampled instead of implying minute resolution.
      const thirtyDays = page.getByRole("button", { name: "30d", exact: true });
      await thirtyDays.click();
      assert.equal(await thirtyDays.getAttribute("aria-pressed"), "true");
      const notice = page.locator('p[role="status"]').filter({ hasText: /6h/ });
      await notice.waitFor();

      // Freshness comes from the real sample time, so a 6h bucket that started
      // hours ago must not be reported as hours stale (R18).
      const describedByLarge = await page.getByRole("slider").first().getAttribute("aria-describedby");
      const largeDescription = await page.locator(`#${describedByLarge}`).textContent();
      const freshness = /freshness (\d+)/.exec(largeDescription);
      assert.ok(freshness, `no freshness in: ${largeDescription}`);
      assert.ok(Number(freshness[1]) <= 5, `freshness = ${freshness[1]} minutes, want the latest sample`);

      // The axis choice changes what the chart plots and how it is labelled.
      await page.getByRole("button", { name: "Throughput", exact: true }).click();
      assert.equal(
        await page.getByRole("button", { name: "Throughput", exact: true }).getAttribute("aria-pressed"),
        "true",
      );

      // The statistic is a page-level choice, and it applies to the cards as
      // well as the chart: the mock reports 30% average but 60% peak for CPU.
      const peak = page.getByRole("button", { name: "Peak", exact: true });
      await peak.click();
      assert.equal(await peak.getAttribute("aria-pressed"), "true");
      await page.getByRole("slider").first().waitFor();
      // The mock reports 30% average and 60% peak for CPU, so the card grid must
      // show both: the peak as the current value and the average under Avg.
      const cards = page.locator("div.grid", { hasText: "CPU Usage" }).first();
      await cards.waitFor();
      await page.waitForTimeout(150);
      const cardText = await cards.textContent();
      assert.match(cardText, /60\.0%/, `the card must follow the peak column: ${cardText}`);
      // The average column stays the average of the average buckets (the mock's
      // first bucket is a real zero), so it must not jump to the peak value.
      assert.match(cardText, /29\.\d%/, `the average column must stay the average: ${cardText}`);

      // Series must stay distinguishable without colour: more than one dash
      // pattern has to be rendered.
      const dashes = await page
        .locator("svg line[stroke-dasharray]")
        .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("stroke-dasharray")));
      assert.ok(
        new Set(dashes.filter(Boolean)).size >= 2,
        `expected distinct dash patterns, got ${JSON.stringify(dashes)}`,
      );

      // The semantic data table carries the same numbers, and a missing bucket
      // reads as a dash rather than as zero.
      await page.getByRole("button", { name: "Show data table", exact: true }).click();
      // The toggle's label flips, so assert the expanded state on the new name.
      const opened = page.getByRole("button", { name: "Hide data table", exact: true });
      assert.equal(await opened.getAttribute("aria-expanded"), "true");
      const table = page.locator("#monitoring-data-table table");
      await table.waitFor();
      assert.equal(await table.locator("caption").count(), 1);
      assert.ok((await table.locator("thead th").count()) >= 2);
      const rows = await table.locator("tbody tr").count();
      assert.ok(rows > 1, `expected multiple rows, got ${rows}`);
      // Switch back to the percentage axis to assert the zero-versus-missing
      // distinction on values that are not rates: bucket 0 is a real zero and
      // bucket 1 was deliberately omitted from the mock.
      await page.getByRole("button", { name: "Usage (%)", exact: true }).click();
      await page.getByRole("button", { name: "Average", exact: true }).click();
      await page.waitForTimeout(200);
      const percentTable = page.locator("#monitoring-data-table table");
      const firstRow = await percentTable.locator("tbody tr").nth(0).locator("td").allInnerTexts();
      assert.ok(
        firstRow.some((cell) => cell.trim() === "0.0%"),
        `a real zero must stay visible as 0: ${JSON.stringify(firstRow)}`,
      );
      const secondRow = await percentTable.locator("tbody tr").nth(1).locator("td").allInnerTexts();
      assert.ok(
        secondRow.every((cell) => cell.trim() !== "0.0%"),
        `a missing bucket must not become zero: ${JSON.stringify(secondRow)}`,
      );
    },
  );

  await test(
    "monitoring controls stay reachable and touch-sized at 375px",
    async ({ page, goto }) => {
      await goto("/servers/1");
      for (const name of ["15m", "Usage (%)", "Average"]) {
        const control = page.getByRole("button", { name, exact: true });
        await control.waitFor();
        const box = await control.boundingBox();
        assert.ok(box && box.height >= 44, `${name} is only ${box?.height}px tall, want >= 44px`);
      }
    },
    true,
  );

  await test("series toggles, chart description and the paged data view are keyboard accessible", async ({
    page,
    goto,
  }) => {
    await goto("/servers/1");
    const chart = page.getByRole("slider").first();
    await chart.waitFor();

    // The chart announces its own configuration (§4.5): range, granularity,
    // statistic, unit and freshness, not just a usage hint.
    const describedBy = await chart.getAttribute("aria-describedby");
    const description = await page.locator(`#${describedBy}`).textContent();
    for (const needle of ["granularity", "statistic", "unit", "freshness"]) {
      assert.ok(description.includes(needle), `chart description lacks "${needle}": ${description}`);
    }
    assert.match(description, /granularity 1m/, description);

    // Series toggles: a keyboard-operable group that exposes its pressed state
    // and actually removes the series from the plot.
    const toggle = page.getByRole("button", { name: "CPU", exact: true });
    assert.equal(await toggle.getAttribute("aria-pressed"), "true");
    const pathsBefore = await page.locator('svg path[fill="none"]').count();
    await toggle.click();
    assert.equal(await toggle.getAttribute("aria-pressed"), "false");
    assert.equal(
      await page.locator('svg path[fill="none"]').count(),
      pathsBefore - 1,
      "a hidden series must stop being drawn",
    );
    await toggle.focus();
    await page.keyboard.press("Space");
    assert.equal(await toggle.getAttribute("aria-pressed"), "true", "Space must toggle the series");
    assert.equal(await page.locator('svg path[fill="none"]').count(), pathsBefore);

    // The data view pages at 50 rows and names what it contains.
    await page.getByRole("button", { name: "7d", exact: true }).click();
    await page.getByRole("button", { name: "Show data table", exact: true }).click();
    const table = page.locator("#monitoring-data-table table");
    await table.waitFor();
    const caption = await table.locator("caption").textContent();
    for (const needle of ["granularity", "statistic", "unit"]) {
      assert.ok(caption.includes(needle), `caption lacks "${needle}": ${caption}`);
    }
    const headers = await table.locator("thead th").allInnerTexts();
    assert.ok(headers.includes("Coverage"), `coverage column missing: ${JSON.stringify(headers)}`);

    const firstPageRows = await table.locator("tbody tr").count();
    assert.equal(firstPageRows, 50, "the first page holds exactly 50 rows");
    const firstCell = await table.locator("tbody tr").first().locator("td").last().innerText();
    assert.match(firstCell, /%|sampled|no sample/, `coverage cell = ${firstCell}`);

    const pager = page.getByRole("button", { name: "Next page", exact: true });
    await pager.click();
    await page.waitForTimeout(100);
    assert.equal(await table.locator("tbody tr").count(), 50);
    assert.match(await page.locator('span[aria-live="polite"]').first().innerText(), /Page 2 of \d+/);

    // Paging to the end keeps the remainder and disables Next.
    let guard = 0;
    while (!(await pager.isDisabled()) && guard++ < 10) {
      await pager.click();
      await page.waitForTimeout(50);
    }
    const lastPageRows = await table.locator("tbody tr").count();
    assert.ok(lastPageRows > 0 && lastPageRows <= 50, `last page rows = ${lastPageRows}`);
    assert.equal(await pager.isDisabled(), true, "Next must be disabled on the last page");
    assert.equal(
      await page.getByRole("button", { name: "Previous page", exact: true }).isEnabled(),
      true,
    );
  });

  await test("a late response from the previous range never replaces the current one", async ({
    page,
    state,
    goto,
  }) => {
    const pending = gate();
    let held = false;
    state.custom = async ({ path, url, reply }) => {
      if (!/\/metrics$/.test(path)) return false;
      // Hold the first (1h/1m) response until after the 6h request resolves.
      if (!held && url.searchParams.get("interval") === "1m") {
        held = true;
        await pending.promise;
        const interval = 60000;
        const first = Math.floor(Date.parse(url.searchParams.get("from")) / interval) * interval;
        const points = [];
        for (let time = first; time <= Date.parse(url.searchParams.get("to")); time += interval) {
          points.push({
            time: new Date(time).toISOString(),
            cpu_percent: 99,
            memory_percent: 99,
            disk_percent: 99,
            swap_percent: 99,
            load_1: 9,
            load_5: 9,
            load_15: 9,
            net_recv_rate: 9,
            net_sent_rate: 9,
            disk_read_rate: 9,
            disk_write_rate: 9,
          });
        }
        await reply({
          server_id: 1,
          from: url.searchParams.get("from"),
          to: url.searchParams.get("to"),
          requested_interval: "1m",
          actual_interval: "1m",
          coarsen_reason: "",
          stat: "avg",
          coverage: 1,
          source: "raw",
          downsampled: false,
          points,
        });
        return true;
      }
      return false;
    };

    await goto("/servers/1");
    await page.getByRole("button", { name: "6h", exact: true }).click();
    // Let the newer request settle, then release the stale one.
    await page.getByRole("slider").first().waitFor();
    pending.release();
    await page.waitForTimeout(200);

    // The chart must still describe the 6h window: the stale 99% series would
    // produce value labels far above the mocked 30%.
    await page.getByRole("button", { name: "Show data table", exact: true }).click();
    const table = page.locator("#monitoring-data-table table");
    await table.waitFor();
    const cells = await table.locator("tbody tr").last().locator("td").allInnerTexts();
    assert.ok(
      cells.every((cell) => !cell.includes("99")),
      `stale response leaked into the table: ${JSON.stringify(cells)}`,
    );
  });

  await test("chart keyboard navigation distinguishes gaps/zero and handles range changes", async ({
    page,
    goto,
  }) => {
    await goto("/servers/1");
    const chart = page.getByRole("slider").first();
    await chart.focus();
    await page.keyboard.press("Home");
    assert.equal(await chart.getAttribute("aria-valuenow"), "0");
    // The chart now labels series with the axis vocabulary and formats the value
    // with its unit, so the announcement carries the real zero explicitly.
    assert.match(await chart.getAttribute("aria-valuetext"), /CPU: 0\.0%/);
    await capture(page, "monitoring-keyboard");
    await page.keyboard.press("ArrowRight");
    assert.equal(await chart.getAttribute("aria-valuenow"), "1");
    assert.match(await chart.getAttribute("aria-valuetext"), /No sample/);
    await page.keyboard.press("End");
    assert.equal(
      await chart.getAttribute("aria-valuenow"),
      await chart.getAttribute("aria-valuemax"),
    );
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "7d", exact: true }).click();
    await page.getByRole("slider").first().waitFor();
    assert.equal(
      await page.getByRole("button", { name: "7d", exact: true }).getAttribute("aria-pressed"),
      "true",
    );
  });

  await test(
    "touch chart supports horizontal drag without blocking vertical page scroll",
    async ({ page, context, goto }) => {
      await goto("/servers/1");
      const chart = page.getByRole("slider").first();
      const area = chart.locator('rect[fill="transparent"]');
      await area.scrollIntoViewIfNeeded();
      const box = await area.boundingBox();
      const client = await context.newCDPSession(page);
      const touch = (type, x, y) =>
        client.send("Input.dispatchTouchEvent", {
          type,
          touchPoints: type === "touchEnd" ? [] : [{ x, y, id: 1 }],
        });
      const y = box.y + box.height / 2;
      // A short interior drag exercises chart pointer capture without invoking
      // Chromium's page-level history gesture (which returns to about:blank).
      await touch("touchStart", box.x + box.width * 0.4, y);
      const before = Number(await chart.getAttribute("aria-valuenow"));
      await touch("touchMove", box.x + box.width * 0.6, y);
      await until(
        async () => Number(await chart.getAttribute("aria-valuenow")) > before,
        "touch drag changes selected timestamp",
      );
      await touch("touchEnd");
      assert.equal(new URL(page.url()).pathname, "/servers/1", "chart drag must stay on the server page");
      const scrollBefore = await page.evaluate(() => scrollY);
      await touch("touchStart", box.x + 30, y);
      for (let i = 1; i <= 5; i++) await touch("touchMove", box.x + 30, y - i * 12);
      await touch("touchEnd");
      await until(
        async () => (await page.evaluate(() => scrollY)) > scrollBefore,
        "vertical touch scroll",
      );
    },
    true,
  );

  await test("light/dark link and focus colors retain sufficient contrast", async ({
    page,
    goto,
  }) => {
    await goto("/servers");
    await page.getByRole("button", { name: "Alpha", exact: true }).waitFor();
    const contrast = (a, b) => {
      const luminance = (hex) => {
        hex = hex.replace(/^#([a-f\d])([a-f\d])([a-f\d])$/i, "#$1$1$2$2$3$3");
        const [r, g, b] = hex
          .match(/[a-f\d]{2}/gi)
          .map((v) => parseInt(v, 16) / 255)
          .map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
        return r * 0.2126 + g * 0.7152 + b * 0.0722;
      };
      const [x, y] = [luminance(a), luminance(b)].sort((a, b) => b - a);
      return (x + 0.05) / (y + 0.05);
    };
    for (const dark of [false, true]) {
      const colors = await page.evaluate((dark) => {
        document.documentElement.classList.toggle("dark", dark);
        const style = getComputedStyle(document.documentElement);
        return Object.fromEntries(
          ["link", "link-hover", "ring", "card", "background", "primary-subtle",
           "chart-1", "chart-2", "chart-3", "chart-4"].map((key) => [
            key,
            style.getPropertyValue(`--color-${key}`).trim(),
          ]),
        );
      }, dark);
      for (const background of ["card", "background", "primary-subtle"])
        assert.ok(
          contrast(colors.link, colors[background]) >= 4.5,
          `${colors.link} on ${colors[background]}`,
        );
      assert.ok(contrast(colors.ring, colors.background) >= 3);
      // Informative chart graphics need 3:1 against the surface they are drawn on.
      for (const key of ["chart-1", "chart-2", "chart-3", "chart-4"]) {
        assert.ok(
          contrast(colors[key], colors.card) >= 3,
          `${key} ${colors[key]} on card ${colors.card} (${contrast(colors[key], colors.card).toFixed(2)}:1)`,
        );
      }
    }
  });
} finally {
  if (browser) await browser.close();
  server.kill();
}
nameSuite("ui-regressions");
writeReport({ passed, failed });
console.log(`UI regressions: ${passed} passed, ${failed} failed`);
if (failed) process.exitCode = 1;
