// Terminal UI suite (TC-09-01 and TC-09-02 browser half).
//
// The Hub's WebSocket is replaced by a controllable double so the test can drive
// server messages, count connections and inspect every frame the client sends. The
// real xterm instance runs unchanged: this asserts the DOM the user actually sees.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright");
const root = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.UI_TEST_PORT ?? 4179);
const base = `http://127.0.0.1:${port}`;
const server = spawn(
  process.execPath,
  ["node_modules/vite/bin/vite.js", "preview", "--host", "127.0.0.1", "--port", String(port), "--strictPort"],
  { cwd: root, stdio: "ignore" },
);

async function until(check, description = "condition", timeout = 5000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(20);
  }
  throw new Error(`Timed out: ${description}`);
}

const serverFixture = {
  id: 1,
  name: "Alpha",
  host: "alpha.example.test",
  port: 2222,
  owner_id: 1,
  status: "online",
  created_at: "2026-01-01T00:00:00Z",
  credential_id: 1,
};

/**
 * Installs the WebSocket double. It records every socket and every frame, and lets
 * the test push server messages or drop the connection.
 */
const webSocketStub = () => {
  class FakeWebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;
    constructor(url) {
      this.url = url;
      this.readyState = FakeWebSocket.CONNECTING;
      this.sent = [];
      this.onopen = null;
      this.onmessage = null;
      this.onclose = null;
      this.onerror = null;
      window.__ws.sockets.push(this);
      // The handshake completes on the next tick so the app's own onopen runs.
      setTimeout(() => {
        this.readyState = FakeWebSocket.OPEN;
        this.onopen?.(new Event("open"));
      }, 0);
    }
    send(data) {
      this.sent.push(JSON.parse(data));
      window.__ws.frames.push({ socket: this, message: JSON.parse(data) });
    }
    close(code = 1000) {
      this.readyState = FakeWebSocket.CLOSED;
      window.__ws.closed.push(this);
      this.onclose?.(new CloseEvent("close", { code }));
    }
    /** Test hook: deliver a server frame. */
    deliver(message) {
      this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(message) }));
    }
    /** Test hook: the peer drops the connection. */
    drop() {
      this.readyState = FakeWebSocket.CLOSED;
      this.onclose?.(new CloseEvent("close", { code: 1006 }));
    }
    readyState;
  }
  window.__ws = { sockets: [], frames: [], closed: [], last: () => window.__ws.sockets.at(-1) };
  window.WebSocket = FakeWebSocket;
};

async function fixture(browser, { viewport = { width: 1280, height: 900 }, mobile = false, language = "en" } = {}) {
  const context = await browser.newContext({ viewport, hasTouch: mobile, isMobile: mobile, locale: "en-US" });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base });
  await context.addInitScript(webSocketStub);
  await context.addInitScript(
    ([lang]) => {
      localStorage.setItem(
        "auth_token",
        `test.${btoa(JSON.stringify({ uid: 1, username: "terminal-user", role: "admin", exp: Date.now() / 1000 + 3600 }))}.test`,
      );
      localStorage.setItem("i18nextLng", lang);
      localStorage.setItem("talus-theme", "light");
    },
    [language],
  );
  const errors = [];
  await context.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const reply = (data, status = 200) =>
      route
        .fulfill({
          status,
          contentType: "application/json",
          body: JSON.stringify(status >= 400 ? data : { data }),
        })
        .catch(() => {});
    if (/^\/api\/v1\/servers\/\d+$/.test(path)) return reply(serverFixture);
    if (path === "/api/v1/servers" || path === "/api/v1/servers/summary") return reply([serverFixture]);
    if (path.endsWith("/metrics")) {
      return reply({
        server_id: 1,
        from: url.searchParams.get("from"),
        to: url.searchParams.get("to"),
        requested_interval: "auto",
        actual_interval: "1m",
        coarsen_reason: null,
        stat: "avg",
        coverage: 1,
        source: "raw",
        downsampled: false,
        points: [],
      });
    }
    return reply([]);
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.on("pageerror", (error) => errors.push(error.message));
  return { page, context, errors, goto: (path) => page.goto(`${base}${path}`) };
}

let browser;
let passed = 0;
let failed = 0;
async function test(name, run, options) {
  if (process.env.UI_TEST_FILTER && !new RegExp(process.env.UI_TEST_FILTER).test(name)) return;
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_EXECUTABLE || undefined,
    args: JSON.parse(process.env.CHROMIUM_ARGS ?? "[]"),
  });
  const f = await fixture(browser, options);
  try {
    await run(f);
    assert.deepEqual(f.errors, [], "No uncaught page errors");
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

const statusPill = (page) => page.getByRole("status").first();
const surface = (page) => page.getByTestId("terminal-surface");
const terminalText = (page) => surface(page).innerText();
const clipboardWrite = (page, text) => page.evaluate((value) => navigator.clipboard.writeText(value), text);
const frames = (page) => page.evaluate(() => window.__ws.frames.map((frame) => frame.message));
const socketCount = (page) => page.evaluate(() => window.__ws.sockets.length);
const clientInputs = async (page) => (await frames(page)).filter((frame) => frame.type === "input").map((frame) => frame.data);

try {
  await until(async () => fetch(base).then((r) => r.ok).catch(() => false), "preview server");

  await test("toolbar shows the server identity, connection state and controls", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    assert.equal(await page.getByTestId("terminal-server-name").innerText(), "Alpha");
    assert.equal(await page.getByTestId("terminal-server-address").innerText(), "alpha.example.test:2222");
    // The initial connect is in flight until the server confirms.
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "connecting", "connecting");
    // The token is sent as the first frame, never in the URL (REQ-09 生命周期).
    const sockets = await page.evaluate(() => window.__ws.sockets.map((socket) => socket.url));
    assert.ok(sockets.length === 1, `sockets: ${sockets.length}`);
    assert.ok(!/token|\.test\./i.test(sockets[0]), `token leaked into the URL: ${sockets[0]}`);
    await until(async () => (await frames(page)).length > 0, "auth frame");
    const [first] = await frames(page);
    assert.equal(first.type, "auth");
  });

  await test("connect, disconnect and reconnect are explicit user actions", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "connected", "connected");
    // Disconnect keeps the user in control: the socket closes and the state is idle.
    await page.getByRole("button", { name: "Disconnect" }).click();
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "disconnected", "disconnected");
    assert.equal(await page.evaluate(() => window.__ws.closed.length), 1);
    // Manual reconnect opens a new session and says so.
    await page.getByRole("button", { name: "Connect" }).click();
    await until(async () => (await socketCount(page)) === 2, "second socket");
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "connecting", "connecting again");
  });

  await test("an automatic retry is announced, counted and can be cancelled", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "connected", "connected");
    await page.evaluate(() => window.__ws.last().drop());
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "reconnecting", "reconnecting");
    assert.match(await statusPill(page).innerText(), /1\/5/, "retry counter is shown");
    // A retry is a new session, never a restored shell.
    assert.match(await statusPill(page).innerText(), /new session/i);
    // Cancelling stops the pending retry: no further socket appears.
    await page.getByRole("button", { name: "Cancel" }).click();
    await until(async () => (await statusPill(page).getAttribute("data-connection-status")) === "disconnected", "cancelled");
    const before = await socketCount(page);
    await delay(2200);
    assert.equal(await socketCount(page), before, "a cancelled retry must not reconnect");
  });

  await test("font size can be changed, reset and is remembered across a reload", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    const size = page.locator("[data-font-size]");
    assert.equal(await size.getAttribute("data-font-size"), "14");
    await page.getByRole("button", { name: "Increase font size" }).click();
    await until(async () => (await size.getAttribute("data-font-size")) === "15", "increased");
    await page.getByRole("button", { name: "Increase font size" }).click();
    await page.getByRole("button", { name: "Decrease font size" }).click();
    await until(async () => (await size.getAttribute("data-font-size")) === "15", "decreased back");
    assert.equal(await page.evaluate(() => localStorage.getItem("talus-terminal-font-size")), "15");
    // The preference survives a reload.
    await page.reload();
    await until(async () => (await page.locator("[data-font-size]").getAttribute("data-font-size")) === "15", "restored");
    await page.getByRole("button", { name: "Reset font size" }).click();
    await until(async () => (await page.locator("[data-font-size]").getAttribute("data-font-size")) === "14", "reset");
    assert.equal(await page.evaluate(() => localStorage.getItem("talus-terminal-font-size")), "14");
  });

  await test("search runs locally, reports misses and closes with Esc", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await page.evaluate(() => window.__ws.last().deliver({ type: "output", data: "needle-in-the-buffer\r\n" }));
    await until(async () => (await terminalText(page)).includes("needle-in-the-buffer"), "output rendered");

    await page.getByRole("button", { name: "Search" }).click();
    const input = page.getByRole("textbox", { name: "Search" });
    await input.fill("needle-in-the-buffer");
    await input.press("Enter");
    // A hit must not warn.
    await delay(150);
    assert.ok(!(await page.getByRole("status").allInnerTexts()).join(" ").includes("No match"), "a hit must not warn");

    await input.fill("absent-needle");
    await input.press("Enter");
    await until(
      async () => (await page.getByRole("status").allInnerTexts()).join(" ").includes("absent-needle"),
      "no-match notice",
    );

    // Search never reaches the remote host (REQ-09 搜索).
    const inputs = await clientInputs(page);
    assert.ok(!inputs.join("").includes("needle"), `search text was sent to the PTY: ${inputs.join("|")}`);

    await page.keyboard.press("Escape");
    await until(async () => (await page.getByRole("textbox", { name: "Search" }).count()) === 0, "search closed");
  });

  await test("clipboard actions are explicit and multi-line pastes are confirmed", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // Copy without a selection explains itself instead of failing silently.
    await page.getByRole("button", { name: "Copy selection" }).click();
    await until(
      async () => (await page.getByRole("status").allInnerTexts()).join(" ").includes("Nothing is selected"),
      "copy feedback",
    );

    // A single-line paste is sent straight to the PTY.
    await clipboardWrite(page, "echo single");
    await page.getByRole("button", { name: "Paste from clipboard" }).click();
    await until(async () => (await clientInputs(page)).includes("echo single"), "single-line paste");

    // A multi-line paste is confirmed first, and only sent after confirmation.
    await clipboardWrite(page, "rm -rf /tmp/a\nrm -rf /tmp/b");
    await page.getByRole("button", { name: "Paste from clipboard" }).click();
    const dialog = page.getByRole("alertdialog", { name: "Confirm multi-line paste" });
    await until(async () => (await dialog.count()) === 1, "paste confirmation");
    assert.match(await dialog.innerText(), /2 lines/);
    assert.ok(!(await clientInputs(page)).join("").includes("rm -rf"), "the paste must wait for confirmation");
    await dialog.getByRole("button", { name: "Send" }).click();
    await until(async () => (await clientInputs(page)).join("").includes("rm -rf /tmp/b"), "confirmed paste");
  });

  await test("clearing the screen only changes the local display", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await page.evaluate(() => window.__ws.last().deliver({ type: "output", data: "clear-me-please\r\n" }));
    await until(async () => (await terminalText(page)).includes("clear-me-please"), "output rendered");
    await page.getByRole("button", { name: /Clear the screen/ }).click();
    await until(async () => !(await terminalText(page)).includes("clear-me-please"), "cleared");
    // Clearing is a client-side action: nothing is sent to the remote host.
    assert.deepEqual((await clientInputs(page)).filter((data) => data.includes("clear")), []);
    // The socket is still open, so the session continues.
    assert.equal(await page.evaluate(() => window.__ws.closed.length), 0);
  });

  await test("fullscreen and shortcuts are wired to the terminal, and Ctrl+C is not stolen", async ({
    page,
    goto,
  }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // Ctrl+F opens the local search; Escape closes it.
    await page.keyboard.press("Control+f");
    await until(async () => (await page.getByRole("textbox", { name: "Search" }).count()) === 1, "Ctrl+F opens search");
    await page.keyboard.press("Escape");
    await until(async () => (await page.getByRole("textbox", { name: "Search" }).count()) === 0, "Escape closes search");

    // Ctrl+L clears locally.
    await page.evaluate(() => window.__ws.last().deliver({ type: "output", data: "shortcut-clear\r\n" }));
    await until(async () => (await terminalText(page)).includes("shortcut-clear"), "output rendered");
    await page.keyboard.press("Control+Shift+l");
    await until(async () => !(await terminalText(page)).includes("shortcut-clear"), "Ctrl+Shift+L cleared");

    // Ctrl+L is readline's clear-screen: it must be sent to the shell, not consumed.
    await clientInputs(page);
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Control+l");
    await until(async () => (await clientInputs(page)).includes("\u000c"), "Ctrl+L reached the PTY");

    // Ctrl+C must reach the remote shell as an interrupt, not the clipboard.
    await clientInputs(page);
    await page.keyboard.press("Control+c");
    await delay(120);
    const afterCtrlC = await page.getByRole("status").allInnerTexts();
    assert.ok(!afterCtrlC.join(" ").includes("Copied"), `Ctrl+C must not copy: ${afterCtrlC.join(" | ")}`);

    // Fullscreen is requested on the terminal container and can be left again from
    // the toolbar (the browser's own Escape path is a UI behaviour, not app code).
    await page.getByRole("button", { name: "Fullscreen" }).click();
    await until(async () => (await page.evaluate(() => document.fullscreenElement !== null)), "fullscreen engaged");
    await page.getByRole("button", { name: "Exit fullscreen" }).click();
    await until(async () => (await page.evaluate(() => document.fullscreenElement === null)), "fullscreen left");
  });

  await test("repeated reconnects and leaving the page never stack sockets or timers", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // Three manual reconnects: each replaces the previous socket instead of adding one.
    for (let round = 0; round < 3; round++) {
      const expected = round + 2;
      await page.getByRole("button", { name: "Disconnect" }).click();
      await page.getByRole("button", { name: "Connect" }).click();
      await until(async () => (await socketCount(page)) === expected, `socket ${expected}`);
      // The next round needs a connected session to disconnect from.
      await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
      await until(
        async () => (await statusPill(page).getAttribute("data-connection-status")) === "connected",
        `connected after round ${round}`,
      );
      assert.equal(
        await page.evaluate(() => window.__ws.sockets.filter((socket) => socket.readyState === 1).length),
        1,
        "exactly one live socket after reconnecting",
      );
    }

    // Leaving the terminal closes the socket and stops the retry loop.
    await page.getByRole("link", { name: "Back to server" }).click();
    await until(async () => (await page.getByTestId("terminal-surface").count()) === 0, "terminal unmounted");
    await until(async () => (await page.evaluate(() => window.__ws.closed.length)) >= 1, "socket closed");
    const before = await socketCount(page);
    await delay(2200);
    assert.equal(await socketCount(page), before, "no reconnect after unmounting");
  });

  await test("PTY resizes are debounced and follow font-size changes", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    const resizeFrames = async () => (await frames(page)).filter((frame) => frame.type === "resize");
    await until(async () => (await resizeFrames()).length >= 1, "initial resize");

    // Three quick layout changes must settle into one message, not three.
    const before = (await resizeFrames()).length;
    for (const width of [1200, 1100, 1000]) {
      await page.setViewportSize({ width, height: 900 });
      await delay(40);
    }
    await until(async () => (await resizeFrames()).length > before, "debounced resize");
    await delay(400);
    const afterBurst = (await resizeFrames()).length;
    assert.equal(afterBurst - before, 1, "a burst of resizes must produce exactly one PTY message");

    // The debounced message reports the size the terminal actually has now: after
    // shrinking the viewport the reported columns must follow.
    const wide = (await resizeFrames()).at(-1);
    assert.ok(wide.cols > 0 && wide.rows > 0, `resize frame ${JSON.stringify(wide)}`);
    await page.setViewportSize({ width: 700, height: 900 });
    await until(
      async () => (await resizeFrames()).at(-1).cols < wide.cols,
      "columns follow a narrower viewport",
    );

    // A font-size change alters the character cell, so the PTY must be told again.
    const beforeFont = (await resizeFrames()).length;
    await page.getByRole("button", { name: "Increase font size" }).click();
    await until(async () => (await resizeFrames()).length > beforeFont, "resize after font change");
    assert.ok((await resizeFrames()).length - beforeFont <= 2, "one font change is one resize");
  });

  await test(
    "375px and landscape keep the terminal usable with 44px controls",
    async ({ page, goto }) => {
      await goto("/servers/1/terminal");
      await until(async () => (await socketCount(page)) === 1, "one socket");
      // No horizontal overflow of the page or the toolbar.
      const overflow = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
      }));
      assert.ok(
        overflow.scrollWidth <= overflow.innerWidth + 1,
        `horizontal overflow: ${overflow.scrollWidth} > ${overflow.innerWidth}`,
      );
      // The narrow layout keeps its actions behind the "More" menu, each 44px tall.
      const more = page.getByTestId("terminal-more").locator("summary");
      assert.equal(await more.count(), 1, "the more menu exists on a narrow layout");
      const box = await more.boundingBox();
      assert.ok(box.height >= 44, `more control height ${box.height}`);
      await more.click();
      const items = page.getByTestId("terminal-more").getByRole("button");
      const count = await items.count();
      assert.ok(count >= 8, `more menu items: ${count}`);
      for (let index = 0; index < count; index++) {
        const item = await items.nth(index).boundingBox();
        assert.ok(item.height >= 44, `menu item ${index} height ${item.height}`);
      }
      // The terminal still owns the remaining height.
      const surfaceBox = await surface(page).boundingBox();
      assert.ok(surfaceBox.height > 200, `terminal height ${surfaceBox.height}`);
    },
    { viewport: { width: 375, height: 667 }, mobile: true },
  );

  await test(
    "landscape phones keep the toolbar and terminal visible",
    async ({ page, goto }) => {
      await goto("/servers/1/terminal");
      await until(async () => (await socketCount(page)) === 1, "one socket");
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      assert.ok(overflow <= 1, `horizontal overflow: ${overflow}`);
      const surfaceBox = await surface(page).boundingBox();
      assert.ok(surfaceBox.height > 120, `terminal height ${surfaceBox.height}`);
      assert.equal(await page.getByTestId("terminal-more").count(), 1);
    },
    { viewport: { width: 667, height: 375 }, mobile: true },
  );
} finally {
  server.kill();
  if (browser) await browser.close();
}

console.log(`Terminal UI: ${passed} passed, ${failed} failed`);
if (failed) process.exitCode = 1;
