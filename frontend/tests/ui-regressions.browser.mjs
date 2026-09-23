// Optional browser regression suite. Build first; all API traffic is mocked.
// See tests/README.md for browser setup and environment overrides.
import assert from "node:assert/strict";
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
      const interval = { "1m": 60000, "5m": 300000, "15m": 900000, "1h": 3600000 }[
        url.searchParams.get("interval")
      ];
      const first = Math.floor(Date.parse(url.searchParams.get("from")) / interval) * interval;
      const last = Date.parse(url.searchParams.get("to"));
      const points = [];
      for (let time = first, index = 0; time <= last; time += interval, index++) {
        if (index === 1) continue; // A gap must remain distinct from real zero.
        points.push({
          time: new Date(time).toISOString(),
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
        });
      }
      return reply(points);
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
  page.setDefaultTimeout(5000);
  page.on("pageerror", (error) => state.errors.push(error.message));
  return { page, context, state, goto: (path) => page.goto(`${base}${path}`) };
}

let browser;
let passed = 0;
let failed = 0;
async function test(name, run, mobile = false) {
  if (process.env.UI_TEST_FILTER && !new RegExp(process.env.UI_TEST_FILTER).test(name)) return;
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

  await test("chart keyboard navigation distinguishes gaps/zero and handles range changes", async ({
    page,
    goto,
  }) => {
    await goto("/servers/1");
    const chart = page.getByRole("slider").first();
    await chart.focus();
    await page.keyboard.press("Home");
    assert.equal(await chart.getAttribute("aria-valuenow"), "0");
    assert.match(await chart.getAttribute("aria-valuetext"), /CPU Usage: 0/);
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
    await page.getByRole("button", { name: "7 Days", exact: true }).click();
    await page.getByRole("slider").first().waitFor();
    assert.equal(
      await page.getByRole("button", { name: "7 Days", exact: true }).getAttribute("aria-pressed"),
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
      await touch("touchStart", box.x + 10, y);
      const before = Number(await chart.getAttribute("aria-valuenow"));
      await touch("touchMove", box.x + box.width - 10, y);
      await until(
        async () => Number(await chart.getAttribute("aria-valuenow")) > before,
        "touch drag changes selected timestamp",
      );
      await touch("touchEnd");
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
          ["link", "link-hover", "ring", "card", "background", "primary-subtle"].map((key) => [
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
    }
  });
} finally {
  if (browser) await browser.close();
  server.kill();
}
console.log(`UI regressions: ${passed} passed, ${failed} failed`);
if (failed) process.exitCode = 1;
