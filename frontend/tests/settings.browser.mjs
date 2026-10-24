// Settings UI against mock APIs; real-DB Go tests cover backend auth/persistence.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE ?? "playwright"
);
const root = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.UI_TEST_PORT ?? 4181),
  base = `http://127.0.0.1:${port}`;
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
let output = "";
server.stdout.on("data", (d) => {
  output += d;
});
server.stderr.on("data", (d) => {
  output += d;
});
async function until(check, description = "condition") {
  const end = Date.now() + 10000;
  while (Date.now() < end) {
    if (await check()) return;
    await delay(20);
  }
  throw new Error(`Timed out: ${description}`);
}
function initialSettings() {
  const rows = [
    ["AGENT_HUB_URL", "monitoring", "https://deployment.example", "agent"],
    ["MONITOR_INTERVAL", "monitoring", 5, "agent", 1, 60],
    ["AGENT_ALLOW_HTTP", "monitoring", true, "agent"],
    ["SSH_TIMEOUT", "ssh", 10, "next_operation", 1, 300],
    ["SSH_MAX_IDLE", "ssh", 300, "next_operation", 1, 86400],
    ["EXEC_TIMEOUT", "execution", 30, "next_operation", 1, 300],
    ["EXEC_TIMEOUT_MAX", "execution", 300, "next_operation", 1, 300],
    ["EXEC_OUTPUT_LIMIT", "execution", 8388608, "next_operation", 1, 8388608],
    [
      "EXEC_OUTPUT_STREAM_LIMIT",
      "execution",
      4194304,
      "next_operation",
      1,
      4194304,
    ],
    ["USAGE_LOG_RETENTION_DAYS", "retention", 30, "maintenance", 1, 36500],
    [
      "USAGE_LOG_SENSITIVE_RETENTION_DAYS",
      "retention",
      90,
      "maintenance",
      1,
      36500,
    ],
    ["METRICS_RETENTION_ENABLED", "retention", false, "maintenance"],
    ["METRICS_RETENTION_DRY_RUN", "retention", true, "maintenance"],
    ["LOGIN_RATE_LIMIT", "security", 10, "live", 1, 10000],
    [
      "LOG_LEVEL",
      "logging",
      "info",
      "live",
      undefined,
      undefined,
      ["debug", "info", "warn", "error"],
    ],
    [
      "LOG_FORMAT",
      "logging",
      "json",
      "live",
      undefined,
      undefined,
      ["json", "text"],
    ],
    ["DB_MAX_OPEN_CONNECTIONS", "capacity", 32, "restart", 12, 10000],
    ["USAGE_LOG_WRITE_CONCURRENCY", "capacity", 4, "restart", 1, 10000],
    ["USAGE_LOG_ACTIVE_LIMIT", "capacity", 4096, "restart", 1, 1000000],
  ];
  return {
    revision: 1,
    migration: { state: "waiting" },
    settings: rows.map(([key, group, value, apply, min, max, choices]) => ({
      key,
      group,
      value,
      apply,
      min,
      max,
      choices,
      kind: typeof value,
      effective_value: value,
      fallback_value: value,
      source: "environment",
      fallback_source: "environment",
      restart_required: false,
    })),
  };
}
async function fixture(
  browser,
  { role = "admin", mobile = false, language = "en", readDelay = 0 } = {},
) {
  const context = await browser.newContext({
    viewport: mobile
      ? { width: 390, height: 844 }
      : { width: 1280, height: 900 },
    locale: language,
  });
  await context.addInitScript(
    ({ role, language }) => {
      localStorage.setItem(
        "auth_token",
        `test.${btoa(JSON.stringify({ uid: 1, username: "settings-user", role, exp: Date.now() / 1000 + 3600 }))}.test`,
      );
      localStorage.setItem("i18nextLng", language);
    },
    { role, language },
  );
  const state = {
    data: initialSettings(),
    writes: [],
    reads: 0,
    errors: [],
    conflict: false,
    invalidKey: null,
    hold: null,
  };
  await context.route("**/api/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    const reply = (data, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(status < 400 ? { data } : data),
      });
    if (path === "/api/v1/settings") {
      if (req.method() === "GET") {
        state.reads++;
        if (readDelay) await delay(readDelay);
        return reply(state.data);
      }
      const patch = req.postDataJSON();
      state.writes.push(patch);
      if (state.hold) await state.hold;
      if (state.invalidKey)
        return reply(
          {
            error: {
              code: 422,
              reason: "settings_invalid",
              params: { key: state.invalidKey },
            },
          },
          422,
        );
      if (state.conflict)
        return reply(
          { error: { code: 409, reason: "conflict", message: "conflict" } },
          409,
        );
      for (const s of state.data.settings)
        if (s.key in patch.values) {
          const v = patch.values[s.key];
          s.value = v === null ? s.fallback_value : v;
          s.source = v === null ? s.fallback_source : "database";
          if (s.apply !== "restart") s.effective_value = s.value;
          s.restart_required = s.value !== s.effective_value;
        }
      state.data.revision++;
      delete state.data.recovery;
      return reply(state.data);
    }
    if (path === "/api/v1/servers/summary")
      return reply([
        {
          id: 1,
          name: "Node A",
          host: "node.example",
          credential_id: 1,
          status: "online",
          monitoring: {
            connection: "online",
            collection: "healthy",
            data: "fresh",
            deployment: "failed",
            deployment_error: "ssh_connection_failed",
            pending_samples: 0,
            dropped_samples: 0,
          },
        },
      ]);
    if (path === "/api/v1/updates")
      return reply({
        current_version: "dev",
        latest_release: null,
        update_available: false,
        checked_at: null,
      });
    return reply({ version: "dev" });
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => state.errors.push(e.message));
  await page.goto(`${base}/settings`);
  if (role === "admin") await page.locator("#AGENT_HUB_URL").waitFor();
  return { page, context, state };
}
const save = (page) =>
  page.getByRole("button", { name: "Save settings", exact: true }).click();
const saved = (page) =>
  until(
    () =>
      page
        .getByRole("button", { name: "Save settings", exact: true })
        .isDisabled(),
    "saved form",
  );
const row = (page, key) => page.locator(`#${key}`).locator("..");
let browser,
  failed = 0;
nameSuite("settings", import.meta.url);
async function test(name, run, options) {
  const f = await fixture(browser, options);
  try {
    await run(f);
    assert.deepEqual(f.state.errors, []);
    recordCase(name);
    console.log(`PASS ${name}`);
  } catch (e) {
    failed++;
    recordCase(name, false);
    console.error(`FAIL ${name}: ${e.stack}`);
  } finally {
    await f.context.close();
  }
}
try {
  await until(async () => {
    try {
      return (await fetch(base)).ok;
    } catch {
      return false;
    }
  }, `preview: ${output}`);
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_EXECUTABLE || undefined,
    args: JSON.parse(process.env.CHROMIUM_ARGS ?? "[]"),
  });
  await test("settings preserve explicit empty and false values and restore deployment defaults", async ({
    page,
    state,
  }) => {
    await page.getByText("Agent deployment failed", { exact: true }).waitFor();
    assert.equal(await page.getByText("Online", { exact: true }).count(), 1);
    assert.equal(
      await page
        .getByRole("link", { name: "Node A", exact: true })
        .getAttribute("href"),
      "/servers/1",
    );
    await page.locator("#AGENT_HUB_URL").fill("");
    await page.locator("#monitoring summary").click();
    await page.locator("#AGENT_ALLOW_HTTP").selectOption("false");
    await save(page);
    await until(() => state.writes.length === 1);
    await saved(page);
    assert.deepEqual(state.writes[0].values, {
      AGENT_HUB_URL: "",
      AGENT_ALLOW_HTTP: false,
    });
    assert.equal(await page.locator("#AGENT_HUB_URL").inputValue(), "");
    assert.equal(await page.locator("#AGENT_ALLOW_HTTP").inputValue(), "false");
    await row(page, "AGENT_HUB_URL")
      .getByRole("button", { name: "Use deployment configuration" })
      .click();
    assert.equal(
      await page.locator("#AGENT_HUB_URL").inputValue(),
      "https://deployment.example",
    );
    await save(page);
    await until(() => state.writes.length === 2);
    await saved(page);
    assert.deepEqual(state.writes[1].values, { AGENT_HUB_URL: null });
    await page.locator("#DB_MAX_OPEN_CONNECTIONS").fill("48");
    await save(page);
    await until(() => state.writes.length === 3);
    await saved(page);
    assert.match(
      await row(page, "DB_MAX_OPEN_CONNECTIONS").textContent(),
      /Currently active: 32/,
    );
    await page.reload();
    await page.locator("#AGENT_HUB_URL").waitFor();
    assert.equal(
      await page.locator("#DB_MAX_OPEN_CONNECTIONS").inputValue(),
      "48",
    );
    assert.equal(
      await page.locator("#USAGE_LOG_LEGACY_WRITERS_DRAINED").count(),
      0,
    );
  });
  await test("settings require destructive retention confirmation and freeze pending saves", async ({
    page,
    state,
  }) => {
    await page.locator("#USAGE_LOG_RETENTION_DAYS").fill("7");
    await save(page);
    const dialog = page.getByRole("dialog", {
      name: "Confirm retention policy change",
    });
    await dialog.waitFor();
    assert.equal(state.writes.length, 0);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    assert.equal(state.writes.length, 0);
    await save(page);
    let release;
    state.hold = new Promise((r) => {
      release = r;
    });
    await dialog.getByRole("button", { name: "Save settings" }).click();
    await until(() => state.writes.length === 1);
    assert.equal(state.writes[0].confirm_retention_change, true);
    assert.equal(await page.locator("#AGENT_HUB_URL").isDisabled(), true);
    release();
    state.hold = null;
    await dialog.waitFor({ state: "hidden" });
    await saved(page);
    await page.locator("#retention summary").click();
    await page.locator("#METRICS_RETENTION_ENABLED").selectOption("true");
    await page.locator("#METRICS_RETENTION_DRY_RUN").selectOption("false");
    await save(page);
    await dialog.waitFor();
    assert.equal(state.writes.length, 1);
    await dialog.getByRole("button", { name: "Cancel" }).click();
  });
  await test("settings conflict retains the draft and cross-field limits reject invalid saves", async ({
    page,
    state,
  }) => {
    await page.locator("#execution summary").click();
    await page.locator("#EXEC_TIMEOUT_MAX").fill("20");
    await save(page);
    await until(async () =>
      (await page.getByRole("alert").textContent()).includes("Default timeout"),
    );
    assert.equal(state.writes.length, 0);
    await page.locator("#EXEC_TIMEOUT_MAX").fill("300");
    await page.locator("#AGENT_HUB_URL").fill("https://edited.example");
    state.conflict = true;
    await save(page);
    await until(async () =>
      (await page.getByRole("alert").textContent()).includes(
        "changed elsewhere",
      ),
    );
    assert.equal(
      await page.locator("#AGENT_HUB_URL").inputValue(),
      "https://edited.example",
    );
    await page
      .getByRole("button", { name: "Reload / discard changes" })
      .click();
    await until(
      async () =>
        (await page.locator("#AGENT_HUB_URL").inputValue()) ===
        "https://deployment.example",
    );
  });
  await test(
    "settings delayed loading honors group hashes",
    async ({ page }) => {
      for (const group of ["retention", "monitoring"]) {
        await page.goto(`${base}/settings#${group}`);
        await page.locator(`#${group}`).waitFor();
        await until(async () => {
          const bounds = await page.locator(`#${group}`).boundingBox();
          return bounds && bounds.y >= -1 && bounds.y < 100;
        }, `scrolled to ${group} after data loaded`);
      }
    },
    { readDelay: 250 },
  );
  await test("settings recovery warning and field errors remain actionable", async ({
    page,
    state,
  }) => {
    state.data.recovery = { revision: 1, key: "MONITOR_INTERVAL" };
    await page.reload();
    await page
      .getByRole("alert")
      .filter({ hasText: "Recovery mode" })
      .waitFor();
    assert.match(
      await page.getByRole("alert").textContent(),
      /saving replaces the original override document/,
    );
    await page.locator("#MONITOR_INTERVAL").fill("10");
    state.invalidKey = "MONITOR_INTERVAL";
    await save(page);
    await page
      .getByRole("alert")
      .filter({
        hasText: "Check the value and allowed range for Sampling interval",
      })
      .waitFor();
    assert.equal(await page.locator("#MONITOR_INTERVAL").inputValue(), "10");
    state.invalidKey = null;
    await save(page);
    await saved(page);
    assert.equal(await page.getByRole("alert").count(), 0);
  });
  await test(
    "settings deny viewers without fetching configuration",
    async ({ page, state }) => {
      await page.getByRole("alert").waitFor();
      assert.match(
        await page.getByRole("alert").textContent(),
        /Only administrators/,
      );
      assert.equal(state.reads, 0);
      assert.equal(await page.locator('a[href="/settings"]').count(), 0);
      assert.equal(await page.locator("#AGENT_HUB_URL").count(), 0);
    },
    { role: "viewer" },
  );
  await test(
    "settings Chinese mobile layout stays within the viewport",
    async ({ page }) => {
      assert.equal(
        await page
          .getByRole("heading", { name: "系统设置", exact: true })
          .count(),
        1,
      );
      assert.equal(await page.locator("input, select").count(), 19);
      await page.locator("#capacity").scrollIntoViewIfNeeded();
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
        true,
      );
      if (process.env.UI_TEST_SCREENSHOTS) {
        await mkdir(process.env.UI_TEST_SCREENSHOTS, { recursive: true });
        await page.screenshot({
          path: `${process.env.UI_TEST_SCREENSHOTS}/settings-mobile.png`,
          fullPage: true,
        });
      }
    },
    { mobile: true, language: "zh-CN" },
  );
} finally {
  writeReport();
  await browser?.close();
  server.kill();
}
if (failed) process.exitCode = 1;
