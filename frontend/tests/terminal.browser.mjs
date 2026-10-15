// Terminal UI suite (TC-09-01 and TC-09-02 browser half).
//
// The Hub's WebSocket is replaced by a controllable double so the test can drive
// server messages, count connections and inspect every frame the client sends. The
// real xterm instance runs unchanged: this asserts the DOM the user actually sees.
import assert from "node:assert/strict";

import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
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
    /** Test hook: the server closes with a code and reason (F07). */
    closeWith(code, reason = "") {
      this.readyState = FakeWebSocket.CLOSED;
      window.__ws.closed.push(this);
      this.onclose?.(new CloseEvent("close", { code, reason }));
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
  page.setDefaultTimeout(15000);
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
    // Evidence is written after the case ran, with its outcome (F12, S03).
    recordCase(name, true);
    passed++;
    console.log(`PASS ${name}`);
  } catch (error) {
    recordCase(name, false);
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

    // Ctrl+F opens the local search; Escape closes it. The shortcut is terminal-local,
    // so the terminal has to hold focus - which is also the case the review reported.
    await page.getByTestId("terminal-surface").click();
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

    // Leaving the terminal closes *this* socket and stops the retry loop: asserting a
    // cumulative close count would accept an earlier socket closing (F13).
    const live = await page.evaluate(() => {
      const socket = window.__ws.sockets.at(-1);
      window.__closedSocket = socket;
      return window.__ws.sockets.indexOf(socket);
    });
    await page.getByRole("link", { name: "Back to server" }).click();
    await until(async () => (await page.getByTestId("terminal-surface").count()) === 0, "terminal unmounted");
    await until(
      async () =>
        await page.evaluate((index) => {
          const socket = window.__ws.sockets[index];
          return socket.readyState === 3 && window.__ws.closed.includes(socket);
        }, live),
      "the socket this terminal opened is closed",
    );
    assert.equal(
      await page.evaluate(() => window.__ws.sockets.filter((s) => s.readyState === 1).length),
      0,
      "no socket is left open after unmounting",
    );
    const before = await socketCount(page);
    await delay(2200);
    assert.equal(await socketCount(page), before, "no reconnect after unmounting");
  });

  // ---------------------------------------------------------- F05 - F08 ----
  await test("a backend-format error keeps its reason readable outside the terminal", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    // The Hub sends {type:"error", data:"..."} - the shape the production handler uses.
    await page.evaluate(() => window.__ws.last().deliver({ type: "error", data: "invalid or expired token" }));
    await until(
      async () => (await page.getByRole("alert").allInnerTexts()).join(" ").includes("invalid or expired token"),
      "the reason is shown outside the terminal",
    );
    const banner = (await page.getByRole("alert").allInnerTexts()).join(" ");
    assert.ok(!banner.includes("Unknown error"), `the real reason must be shown, got: ${banner}`);
  });

  await test("a delayed handshake still synchronizes the initial PTY size", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    const resizeFrames = async () => (await frames(page)).filter((frame) => frame.type === "resize");
    // Nothing was sent while the socket was still connecting.
    assert.equal((await resizeFrames()).length, 0, "no resize before the socket is open");
    // The server confirms the session only after a slow handshake.
    await delay(600);
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(async () => (await resizeFrames()).length >= 1, "the initial size is sent after readiness", 3000);
    const first = (await resizeFrames())[0];
    assert.ok(first.cols > 0 && first.rows > 0, `initial PTY size ${JSON.stringify(first)}`);
  });

  await test("revocation stops the retry loop and keeps its reason", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
      "connected",
    );
    // The Hub announces the revocation, then closes with the policy code.
    await page.evaluate(() => {
      window.__ws.last().deliver({ type: "error", reason: "revoked", data: "session revoked" });
      window.__ws.last().closeWith(4001, "revoked");
    });
    await until(
      async () => (await page.getByRole("alert").allInnerTexts()).join(" ").includes("session revoked"),
      "the revocation reason is visible",
    );
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "disconnected",
      "the terminal reports the session as ended",
    );
    // No automatic retry: waiting past the first backoff must not open a socket.
    const before = await socketCount(page);
    await delay(2600);
    assert.equal(await socketCount(page), before, "a revoked session must not be retried");
    assert.equal(await page.evaluate(() => window.__ws.sockets.filter((s) => s.readyState === 1).length), 0);
  });

  await test("an in-flight reconnect still shows its attempt and new-session hint", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
      "connected",
    );
    await page.evaluate(() => window.__ws.last().drop());
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "reconnecting",
      "waiting for the retry timer",
    );
    // Let the timer fire: the second socket exists but the server has not confirmed.
    await until(async () => (await socketCount(page)) === 2, "the retry socket", 5000);
    const status = await page.locator("[data-connection-status]");
    assert.equal(await status.getAttribute("data-connection-status"), "reconnecting");
    const text = await status.innerText();
    assert.match(text, /1\/5/, `the attempt count must survive the transition: ${text}`);
    assert.match(text, /new session/i, `the new-session hint must survive: ${text}`);
    // And the retry is still cancellable while it is in flight.
    await page.getByRole("button", { name: "Cancel" }).click();
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "disconnected",
      "cancelled in flight",
    );
    await delay(2200);
    assert.equal(await socketCount(page), 2, "cancelling must stop the loop");
  });

  // ---------------------------------------------------------- S04 - S06 ----
  await test("a separate native paste is preserved after a keyboard paste", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    const inputs = async () => (await clientInputs(page)).join("");

    await clipboardWrite(page, "first-shortcut-paste\r");
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Shift+Insert");
    await until(async () => (await inputs()).includes("first-shortcut-paste"), "the keyboard paste");

    // A different paste immediately afterwards is a new user action and must not be
    // swallowed by a dedupe window (S04).
    const prevented = await page.evaluate(() => {
      const textarea = document.querySelector(".xterm-helper-textarea");
      const event = new ClipboardEvent("paste", {
        bubbles: true,
        cancelable: true,
        clipboardData: new DataTransfer(),
      });
      event.clipboardData.setData("text/plain", "distinct-browser-paste\r");
      textarea.dispatchEvent(event);
      return event.defaultPrevented;
    });
    assert.equal(prevented, true, "xterm must not see the paste");
    await until(async () => (await inputs()).includes("distinct-browser-paste"), "the separate paste is sent");
  });

  await test("a surviving selection stays visible when the top anchor is trimmed", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    const rows = page.locator(".xterm-rows");
    const visible = () => rows.innerText();

    const lines = Array.from({ length: 1500 }, (_, i) => `anchor-${String(i).padStart(4, "0")}`).join("\r\n");
    await page.evaluate((payload) => window.__ws.last().deliver({ type: "output", data: `${payload}\r\n` }), lines);
    await until(async () => (await visible()).includes("anchor-1499"), "the newest line is in view");

    // Select the newest content, as a reader does when they copy the last output: the
    // selection survives the next burst (it stays the newest line), while the viewport's
    // top line does not.
    const box = await surface(page).boundingBox();
    const shown = await visible();
    const selected = (shown.match(/anchor-14\d\d/g) ?? []).at(-1) ?? "";
    assert.ok(selected, `a newest line is visible: ${shown.slice(-80)}`);
    await page.mouse.move(box.x + 12, box.y + box.height - 24);
    await page.mouse.down();
    await page.mouse.move(box.x + 150, box.y + box.height - 8, { steps: 6 });
    await page.mouse.up();

    // One frame large enough to trim the whole viewport (1030 lines), exactly the case
    // where the top anchor dies but the selected line is still in the buffer (S05).
    const burst = Array.from({ length: 1030 }, () => "x").join("\r\n");
    await page.evaluate((payload) => window.__ws.last().deliver({ type: "output", data: `${payload}\r\n` }), burst);
    await delay(400);
    const after = await visible();
    assert.ok(
      after.includes(selected),
      `the selected line ${selected} must stay visible after the trim: ${after.slice(0, 80).replace(/\n/g, "|")}`,
    );
  });

  await test("a session that ended normally is not shown as a failure", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
      "connected",
    );
    // The backend's normal end: a disconnected frame and a normal close (S06).
    await page.evaluate(() => {
      window.__ws.last().deliver({ type: "disconnected" });
      window.__ws.last().closeWith(1000, "");
    });
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "disconnected",
      "the terminal reports disconnected",
    );
    const tone = await page.locator("[data-connection-status]").getAttribute("data-tone");
    assert.notEqual(tone, "error", "a normal end must not be reported as a failure");
    assert.deepEqual(await page.getByRole("alert").allInnerTexts(), [], "no failure banner for a normal end");
  });

  // ---------------------------------------------------------- R01 / R02 ----
  await test("each keyboard paste action produces exactly one remote payload", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    const countMarker = async () =>
      (await clientInputs(page)).join("").split("keyboard_once").length - 1;

    // Ctrl+Shift+V: the key handler and the browser's native paste must not both fire.
    await clipboardWrite(page, "echo keyboard_once\r");
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Control+Shift+v");
    await until(async () => (await countMarker()) >= 1, "the first keyboard paste");
    await delay(400);
    assert.equal(await countMarker(), 1, "Ctrl+Shift+V must send the payload exactly once");

    // Shift+Insert, the other native paste combination.
    await clipboardWrite(page, "echo keyboard_once\r");
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Shift+Insert");
    await until(async () => (await countMarker()) >= 2, "the second keyboard paste");
    await delay(400);
    assert.equal(await countMarker(), 2, "Shift+Insert must send the payload exactly once");
  });

  await test("a native paste keeps its own payload and needs no second clipboard read", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // The browser's trusted event carries the text; the clipboard API is then denied,
    // which must not matter (a real user gesture already granted this paste).
    await page.evaluate(() => {
      navigator.clipboard.readText = async () => {
        throw new DOMException("denied", "NotAllowedError");
      };
    });
    const delivered = await page.evaluate(() => {
      const textarea = document.querySelector(".xterm-helper-textarea");
      const event = new ClipboardEvent("paste", {
        bubbles: true,
        cancelable: true,
        clipboardData: new DataTransfer(),
      });
      event.clipboardData.setData("text/plain", "echo trusted-payload\r");
      textarea.dispatchEvent(event);
      return event.defaultPrevented;
    });
    assert.equal(delivered, true, "xterm must not see the native paste");
    await until(
      async () => (await clientInputs(page)).join("").includes("echo trusted-payload"),
      "the event payload is sent",
    );
    assert.ok(
      !(await page.getByRole("status").allInnerTexts()).join(" ").includes("denied"),
      "a delivered payload must not be reported as a denied clipboard read",
    );
  });

  // ---------------------------------------------------------------- F01 / F02 ----
  await test("local editor paste never reaches the remote PTY, and search stays local", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // A paste into the local search box is a local edit: it must never be typed into
    // the shell (the reported defect sent "printf search_only\n" to the PTY).
    await page.getByRole("button", { name: "Search" }).click();
    const input = page.getByRole("textbox", { name: "Search" });
    await input.click();
    await clipboardWrite(page, "printf search_only");
    await page.keyboard.press("Control+Shift+v");
    await until(async () => (await input.inputValue()).includes("printf search_only"), "the search box received the paste");
    assert.deepEqual(
      (await clientInputs(page)).filter((data) => data.includes("printf")),
      [],
      "a paste into the search box must not reach the PTY",
    );

    // Ctrl+F with the terminal focused opens the local search and sends no 0x06.
    await page.keyboard.press("Escape");
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Control+f");
    await until(async () => (await page.getByRole("textbox", { name: "Search" }).count()) === 1, "Ctrl+F opened the local search");
    assert.ok(!(await clientInputs(page)).includes("\u0006"), "Ctrl+F must not be sent to the shell");
  });

  await test("every native multi-line paste waits for confirmation", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // Shift+Insert is the terminal's native paste: it must go through the same
    // confirmation as the toolbar button.
    await clipboardWrite(page, "rm -rf /tmp/native-a\nrm -rf /tmp/native-b");
    await page.getByTestId("terminal-surface").click();
    await page.keyboard.press("Shift+Insert");
    const dialog = page.getByRole("alertdialog", { name: "Confirm multi-line paste" });
    await until(async () => (await dialog.count()) === 1, "confirmation for the native paste");
    assert.match(await dialog.innerText(), /2 lines/);
    assert.ok(!(await clientInputs(page)).join("").includes("native-a"), "the native paste must wait for confirmation");
    await dialog.getByRole("button", { name: "Send" }).click();
    await until(async () => (await clientInputs(page)).join("").includes("native-b"), "confirmed native paste");

    // A paste event delivered straight to xterm's textarea is prevented, so xterm
    // cannot turn it into input behind the confirmation flow.
    const prevented = await page.evaluate(() => {
      const textarea = document.querySelector(".xterm-helper-textarea");
      if (!textarea) return null;
      const event = new ClipboardEvent("paste", { bubbles: true, cancelable: true });
      textarea.dispatchEvent(event);
      return event.defaultPrevented;
    });
    assert.equal(prevented, true, "the native paste event must be cancelled");
  });

  await test("late clipboard results and pending confirmations cannot cross a connection generation", async ({
    page,
    goto,
  }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));

    // A read that resolves after the session was replaced must not be typed.
    await page.evaluate(() => {
      window.__clipboardGate = {};
      window.__clipboardGate.promise = new Promise((resolve) => {
        window.__clipboardGate.resolve = resolve;
      });
      const original = navigator.clipboard.readText.bind(navigator.clipboard);
      navigator.clipboard.readText = async () => {
        await window.__clipboardGate.promise;
        return "late-command-from-old-session";
      };
      window.__restoreClipboard = () => {
        navigator.clipboard.readText = original;
      };
    });
    await page.getByRole("button", { name: "Paste from clipboard" }).click();
    // Replace the session while the read is still pending.
    await page.getByRole("button", { name: "Disconnect" }).click();
    await page.getByRole("button", { name: "Connect" }).click();
    await until(async () => (await socketCount(page)) === 2, "second socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await until(
      async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
      "second session connected",
    );
    await page.evaluate(() => window.__clipboardGate.resolve());
    await delay(300);
    assert.ok(
      !(await clientInputs(page)).join("").includes("late-command-from-old-session"),
      "a late clipboard read must not reach the new shell",
    );
    await page.evaluate(() => window.__restoreClipboard());

    // A confirmation opened before a reconnect is dropped with the session.
    await clipboardWrite(page, "echo stale-one\necho stale-two");
    await page.getByRole("button", { name: "Paste from clipboard" }).click();
    await until(
      async () => (await page.getByRole("alertdialog", { name: "Confirm multi-line paste" }).count()) === 1,
      "confirmation open",
    );
    await page.getByRole("button", { name: "Disconnect" }).click();
    await until(
      async () => (await page.getByRole("alertdialog", { name: "Confirm multi-line paste" }).count()) === 0,
      "the confirmation is cleared with the session",
    );
    await page.getByRole("button", { name: "Connect" }).click();
    await until(async () => (await socketCount(page)) === 3, "third socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await delay(200);
    assert.ok(!(await clientInputs(page)).join("").includes("stale-one"), "the old confirmation must not type into the new shell");
  });

  await test("a rejected or unavailable clipboard write reports failure, not success", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    await page.evaluate(() => window.__ws.last().deliver({ type: "output", data: "select-me-please\r\n" }));
    await until(async () => (await terminalText(page)).includes("select-me-please"), "output rendered");

    // Select the text with a real drag over the terminal.
    const box = await surface(page).boundingBox();
    await page.mouse.move(box.x + 12, box.y + 12);
    await page.mouse.down();
    await page.mouse.move(box.x + 160, box.y + 12, { steps: 8 });
    await page.mouse.up();

    // A denied write must be reported as denied, with no unhandled rejection.
    await page.evaluate(() => {
      window.__restoreWrite = navigator.clipboard.writeText.bind(navigator.clipboard);
      navigator.clipboard.writeText = async () => {
        throw new DOMException("denied", "NotAllowedError");
      };
    });
    await page.getByRole("button", { name: "Copy selection" }).click();
    await until(
      async () => (await page.getByRole("status").allInnerTexts()).join(" ").includes("denied"),
      "a denied copy is reported",
    );
    const statuses = (await page.getByRole("status").allInnerTexts()).join(" ");
    assert.ok(!statuses.includes("Copied"), `a denied write must not report success: ${statuses}`);
    assert.deepEqual(
      (await page.evaluate(() => window.__unhandled ?? [])),
      [],
      "no unhandled rejection was recorded",
    );
    await page.evaluate(() => {
      navigator.clipboard.writeText = window.__restoreWrite;
    });
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

  await test("a selection keeps its scrollback in view while output arrives", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    // The rendered row layer shows the lines the reader is looking at, so the visible
    // text is the observable: it must not jump to the newest output (F11).
    const rows = page.locator(".xterm-rows");
    const visible = () => rows.innerText();

    const lines = Array.from({ length: 200 }, (_, i) => `line-${String(i).padStart(3, "0")}`).join("\r\n");
    await page.evaluate(
      (payload) => window.__ws.last().deliver({ type: "output", data: `${payload}\r\n` }),
      lines,
    );
    await until(async () => (await visible()).includes("line-199"), "the newest line is in view");

    // Scroll back with real wheel input until the oldest line is visible.
    const box = await surface(page).boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    let reachedTop = false;
    for (let i = 0; i < 60 && !reachedTop; i++) {
      await page.mouse.wheel(0, -600);
      await delay(30);
      reachedTop = (await visible()).includes("line-000");
    }
    assert.ok(reachedTop, "the wheel scrolled back into the scrollback");
    assert.ok(!(await visible()).includes("line-199"), "the newest line scrolled out of view");

    // Select text in the scrollback, then let new output arrive.
    await page.mouse.move(box.x + 12, box.y + 12);
    await page.mouse.down();
    await page.mouse.move(box.x + 140, box.y + 40, { steps: 6 });
    await page.mouse.up();
    assert.ok((await page.evaluate(() => window.getSelection()?.toString() ?? "")).length > 0, "text is selected");

    for (let i = 0; i < 5; i++) {
      await page.evaluate((n) => window.__ws.last().deliver({ type: "output", data: `new-output-${n}\r\n` }), i);
      await delay(50);
    }
    await delay(300);
    const stillVisible = await visible();
    assert.ok(
      stillVisible.includes("line-000"),
      `a selection must keep its view; now showing: ${stillVisible.slice(0, 120).replace(/\n/g, "|")}`,
    );
    assert.ok(!stillVisible.includes("new-output-4"), "the reader must not be pulled to the newest output");
  });

  await test("a selection survives the scrollback buffer trimming older lines", async ({ page, goto }) => {
    await goto("/servers/1/terminal");
    await until(async () => (await socketCount(page)) === 1, "one socket");
    await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
    const rows = page.locator(".xterm-rows");
    const visible = () => rows.innerText();

    // More than the 1000-line scrollback cap, so older lines are trimmed as new ones
    // arrive - the case where restoring a raw viewport index shows the wrong text (R04).
    const lines = Array.from({ length: 1500 }, (_, i) => `line-${String(i).padStart(4, "0")}`).join("\r\n");
    await page.evaluate((payload) => window.__ws.last().deliver({ type: "output", data: `${payload}\r\n` }), lines);
    await until(async () => (await visible()).includes("line-1499"), "the newest line is in view");

    const box = await surface(page).boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    let seen = "";
    for (let i = 0; i < 80; i++) {
      await page.mouse.wheel(0, -700);
      await delay(20);
      seen = await visible();
      if (/line-0[0-4]\d\d/.test(seen)) break;
    }
    const anchorLine = (seen.match(/line-\d{4}/)?.[0] ?? "").trim();
    assert.ok(anchorLine, `a scrollback line is visible: ${seen.slice(0, 80)}`);

    // Select over the visible text, then let output arrive.
    await page.mouse.move(box.x + 12, box.y + 12);
    await page.mouse.down();
    await page.mouse.move(box.x + 140, box.y + 40, { steps: 6 });
    await page.mouse.up();
    for (let i = 0; i < 5; i++) {
      await page.evaluate((n) => window.__ws.last().deliver({ type: "output", data: `trim-output-${n}\r\n` }), i);
      await delay(60);
    }
    await delay(300);
    const after = await visible();
    assert.ok(
      after.includes(anchorLine),
      `the anchored line ${anchorLine} must stay in view after trimming; now: ${after.slice(0, 120).replace(/\n/g, "|")}`,
    );
    assert.ok(!after.includes("trim-output-4"), "the reader must not be pulled to the newest output");
  });

  await test(
    "375px and landscape keep the terminal usable with 44px controls",
    async ({ page, goto }) => {
      await goto("/servers/1/terminal");
      await until(async () => (await socketCount(page)) === 1, "one socket");
      await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
      await until(
        async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
        "connected",
      );
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

      // The search bar must stay fully reachable: the reported defect pushed the
      // close button outside a 375px viewport (F09). The "More" menu was opened
      // above, so the search action is already reachable.
      await page.getByTestId("terminal-more").getByRole("button", { name: "Search" }).click();
      await until(async () => (await page.getByRole("textbox", { name: "Search" }).count()) === 1, "search opened");
      const viewport = page.viewportSize();
      for (const name of ["Search", "Previous match", "Next match", "Close search"]) {
        const control = page.getByRole("textbox", { name }) .or(page.getByRole("button", { name })).first();
        const controlBox = await control.boundingBox();
        assert.ok(controlBox, `${name} has no box`);
        assert.ok(
          controlBox.x >= 0 && controlBox.x + controlBox.width <= viewport.width + 0.5,
          `${name} is outside the viewport: x=${controlBox.x} w=${controlBox.width} vw=${viewport.width}`,
        );
        assert.ok(controlBox.height >= 44, `${name} height ${controlBox.height}`);
      }
    },
    { viewport: { width: 375, height: 667 }, mobile: true },
  );

  await test(
    "landscape phones keep the toolbar and terminal visible",
    async ({ page, goto }) => {
      await goto("/servers/1/terminal");
      await until(async () => (await socketCount(page)) === 1, "one socket");
      await page.evaluate(() => window.__ws.last().deliver({ type: "connected" }));
      await until(
        async () => (await page.locator("[data-connection-status]").getAttribute("data-connection-status")) === "connected",
        "connected",
      );
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      assert.ok(overflow <= 1, `horizontal overflow: ${overflow}`);
      const surfaceBox = await surface(page).boundingBox();
      assert.ok(surfaceBox.height > 120, `terminal height ${surfaceBox.height}`);
      assert.equal(await page.getByTestId("terminal-more").count(), 1);
      // A landscape phone is still a touch device: the primary control keeps its 44px
      // target even though the viewport is wider than the density breakpoint (F10).
      // 667px crosses the density breakpoint, so the desktop toolbar is the one on
      // screen - and it is still a touch device, so its controls keep 44px (F10).
      const disconnect = await page.getByRole("button", { name: "Disconnect" }).boundingBox();
      assert.ok(disconnect.height >= 44, `Disconnect height ${disconnect.height} in landscape`);
      const search = await page.getByRole("button", { name: "Search" }).boundingBox();
      assert.ok(search.height >= 44 && search.width >= 44, `Search ${search.height}x${search.width} in landscape`);
    },
    { viewport: { width: 667, height: 375 }, mobile: true },
  );
} finally {
  server.kill();
  if (browser) await browser.close();
}

nameSuite("terminal", import.meta.url);
writeReport({ passed, failed });
console.log(`Terminal UI: ${passed} passed, ${failed} failed`);
if (failed) process.exitCode = 1;
