// SFTP file panel browser acceptance with the real application and binary XHR.
import assert from "node:assert/strict";

import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? "playwright");
const root = fileURLToPath(new URL("../", import.meta.url));
const port = Number(process.env.UI_TEST_PORT ?? 4185);

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
    constructor(url, protocols) {
      this.url = url;
      this.protocols = protocols;
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
    deliverBinary(bytes) {
      this.onmessage?.(new MessageEvent("message", { data: new Uint8Array(bytes).buffer }));
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

async function fixture(browser, { viewport = { width: 1280, height: 900 }, mobile = false, language = "en", role = "admin" } = {}) {
  const context = await browser.newContext({ viewport, hasTouch: mobile, isMobile: mobile, locale: "en-US" });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base });
  await context.addInitScript(webSocketStub);
  await context.addInitScript(() => {
    window.__binaryRequests = [];
    const send = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function(body) {
      if (body instanceof Blob && body.size <= 128) body.arrayBuffer().then(buffer => window.__binaryRequests.push([...new Uint8Array(buffer)]));
      return send.call(this, body);
    };
  });
  await context.addInitScript(
    ([lang, role]) => {
      localStorage.setItem(
        "auth_token",
        `test.${btoa(JSON.stringify({ uid: 1, username: "terminal-user", role, exp: Date.now() / 1000 + 3600 }))}.test`,
      );
      localStorage.setItem("i18nextLng", lang);
      localStorage.setItem("talus-theme", "light");
    },
    [language, role],
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

async function fileScenario(page, { stall = false, stallChunk = 1, conflictText = false, largeDownload = false, changeDownload = false } = {}) {
  const { createHash } = await import("node:crypto");
  const requests = [], pending = [], sessions = new Map(); let stalled = false, downloadStalled = false;
  const entry = (name, kind = "file", parent = "/home/test", size = 4) => ({ name, path: `${parent}/${name}`, kind, size, mode: kind === "directory" ? "drwxr-xr-x" : "-rw-------", modified_at: "2026-10-16T00:00:00Z" });
  const root = [entry("folder ; $x", "directory"), entry("config.txt", "file", "/home/test", largeDownload ? 17 * 1024 * 1024 : 4)];
  const tree = new Map([["/home/test", root], ["/home/test/folder ; $x", []]]);
  let text = "alpha\n", version = "sha256:initial";
  let downloadData = largeDownload ? Buffer.alloc(17 * 1024 * 1024, 7) : Buffer.from([0, 255, 1, 2]);
  const digest = data => createHash("sha256").update(data).digest("hex");
  const put = (path, size) => { const parent = path.slice(0, path.lastIndexOf("/")), name = path.split("/").at(-1); const entries = tree.get(parent) ?? []; const existing = entries.find(item => item.path === path); if (existing) existing.size = size; else entries.push(entry(name, "file", parent, size)); tree.set(parent, entries); };
  await page.route("**/api/v1/servers/1/sftp/**", async route => {
    const req = route.request(), url = new URL(req.url()), action = url.pathname.split("/").at(-1), path = url.searchParams.get("path");
    requests.push({ action, path, method: req.method(), headers: req.headers(), body: req.postDataBuffer(), query: url.searchParams.toString() });
    const reply = (data, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(status >= 400 ? { error: { reason: data } } : { data }) });
    if (action === "entries") {
      const actual = path === "." ? "/home/test" : path;
      return reply({ path: actual, parent: actual.slice(0, actual.lastIndexOf("/")) || "/", entries: tree.get(actual) ?? [], truncated: false });
    }
    if (action === "start") {
      const body = req.postDataJSON();
      if (root.some(item => item.path === body.path) && !body.overwrite) return reply("sftp_exists", 409);
      const token = `upload-${sessions.size + 1}`;
      const session = { token, path: body.path, size: body.size, sha256: body.sha256, offset: 0, expires_at: "2099-01-01T00:00:00Z", chunks: 0 };
      sessions.set(token, session); return reply(session);
    }
    if (action === "status") return reply(sessions.get(req.postDataJSON().token));
    if (action === "chunk") {
      const session = sessions.get(req.headers()["x-upload-token"]);
      session.chunks++;
      if (stall && !stalled && session.chunks === stallChunk) { stalled = true; pending.push(route); return; }
      assert.equal(Number(url.searchParams.get("offset")), session.offset);
      session.offset += req.postDataBuffer().length;
      return reply(session);
    }
    if (action === "complete") { const session = sessions.get(req.postDataJSON().token); put(session.path, session.size); session.completed = true; return reply({ success: true }); }
    if (action === "uploads") { sessions.delete(req.headers()["x-upload-token"]); return reply({ success: true }); }
    if (action === "upload") { put(path, Number(url.searchParams.get("size"))); return reply({ size: Number(url.searchParams.get("size")) }); }
    if (action === "text" && req.method() === "GET") return reply({ path, content: text, version });
    if (action === "text") {
      const body = req.postDataJSON();
      if (conflictText) { conflictText = false; text = "external edit\n"; version = "sha256:external"; return reply("sftp_conflict", 409); }
      assert.equal(body.version, version); text = body.content; version = `sha256:${digest(Buffer.from(text))}`;
      return reply({ path: body.path, content: text, version });
    }
    if (action === "download") {
      const range = req.headers().range?.match(/^bytes=(\d+)-(\d*)$/);
      const etag = `"sha256:${digest(downloadData)}"`;
      if (largeDownload && range && Number(range[1]) > 0 && !downloadStalled) {
        downloadStalled = true; pending.push(route);
        if (changeDownload) downloadData = Buffer.alloc(downloadData.length, 9);
        return;
      }
      if (range && (!req.headers()["if-range"] || req.headers()["if-range"] === etag)) {
        const start = Number(range[1]), end = Math.min(Number(range[2]), downloadData.length - 1);
        return route.fulfill({ status: 206, contentType: "application/octet-stream", headers: { ETag: etag, "Content-Range": `bytes ${start}-${end}/${downloadData.length}` }, body: downloadData.subarray(start, end + 1) });
      }
      return route.fulfill({ status: 200, contentType: "application/octet-stream", headers: { ETag: etag }, body: downloadData });
    }
    if (action === "archive") return route.fulfill({ status: 200, contentType: "application/zip", body: Buffer.from("mock ZIP bytes") });
    if (action === "mkdir") { const body = req.postDataJSON(), parent = body.path.slice(0, body.path.lastIndexOf("/")); const entries = tree.get(parent) ?? []; if (!entries.some(item => item.path === body.path)) entries.push(entry(body.path.split("/").at(-1), "directory", parent)); tree.set(parent, entries); tree.set(body.path, []); return reply({ success: true }); }
    if (action === "rename") { const { from, to } = req.postDataJSON(); const item = root.find(item => item.path === from); item.name = to.split("/").at(-1); item.path = to; return reply({ success: true }); }
    if (action === "entry") { const index = root.findIndex(item => item.path === path); if (index >= 0) root.splice(index, 1); return reply({ success: true }); }
    return reply("sftp_failed", 502);
  });
  return { requests, pending, sessions, downloadBytes: () => downloadData };
}

const openFiles = async (page, goto) => { await goto("/servers/1/terminal"); await page.getByRole("button", { name: "Files", exact: true }).click(); await page.getByTestId("file-entry").first().waitFor(); };

try {
  await until(() => fetch(base).then(r => r.ok).catch(() => false), "preview server");
  await test("file panel loads lazily, closes without reconnecting the terminal and uses encoded paths", async ({ page, goto }) => {
    const scenario = await fileScenario(page);
    await goto("/servers/1/terminal");
    assert.equal(scenario.requests.length, 0);
    await page.getByRole("button", { name: "Files", exact: true }).click();
    await page.getByTestId("file-entry").first().waitFor();
    const count = await socketCount(page);
    await page.getByRole("button", { name: "folder ; $x", exact: true }).click();
    await page.getByText("This directory is empty", { exact: true }).waitFor();
    assert.equal(scenario.requests.at(-1).path, "/home/test/folder ; $x");
    await page.getByRole("button", { name: "Close files", exact: true }).click();
    assert.equal(await socketCount(page), count);
    assert.equal(await page.getByTestId("file-panel").count(), 0);
  });
  await test("file panel and open editor follow the system theme without losing the draft or terminal", async ({ page, goto }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await fileScenario(page); await openFiles(page, goto);
    const background = selector => page.locator(selector).evaluate(node => getComputedStyle(node).backgroundColor);
    assert.equal(await background(".sftp-panel"), "rgb(255, 255, 255)");
    assert.equal(await background(".sftp-path input"), "rgb(255, 255, 255)");
    await page.getByRole("button", { name: "Light", exact: true }).click();
    assert.equal(await background(".sftp-panel"), "rgb(16, 24, 32)");
    await page.getByRole("button", { name: "Dark", exact: true }).click();
    await page.getByRole("button", { name: "Edit text config.txt", exact: true }).click();
    const editor = page.getByRole("textbox", { name: "Edit text", exact: true });
    await editor.fill("unsaved theme draft\n");
    assert.equal(await background(".sftp-editor"), "rgb(255, 255, 255)");
    const count = await socketCount(page);
    await page.emulateMedia({ colorScheme: "dark" });
    await until(async () => await background(".sftp-editor") === "rgb(8, 13, 18)", "dark editor");
    assert.equal(await background(".sftp-panel"), "rgb(16, 24, 32)");
    await page.emulateMedia({ colorScheme: "light" });
    await until(async () => await background(".sftp-editor") === "rgb(255, 255, 255)", "white editor");
    assert.equal(await editor.inputValue(), "unsaved theme draft\n");
    assert.equal(await socketCount(page), count);
  });
  await test("new folder and rename submit structured paths; delete requires confirmation", async ({ page, goto }) => {
    const scenario = await fileScenario(page); await openFiles(page, goto);
    await page.getByRole("button", { name: "New folder", exact: true }).click();
    await page.getByLabel("Folder path", { exact: true }).fill("/home/test/new-folder");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByRole("button", { name: "new-folder", exact: true }).waitFor();
    await page.getByRole("button", { name: "Rename or move config.txt", exact: true }).click();
    await page.getByLabel("Destination path", { exact: true }).fill("/home/test/中文.ini");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByRole("button", { name: "Delete 中文.ini", exact: true }).waitFor();
    await page.getByRole("button", { name: "Delete 中文.ini", exact: true }).click();
    assert.equal(scenario.requests.filter(item => item.method === "DELETE").length, 0);
    await page.getByRole("dialog", { name: "Delete remote entry?", exact: true }).getByRole("button", { name: "Cancel", exact: true }).click();
    await page.getByRole("button", { name: "Delete 中文.ini", exact: true }).click();
    await page.getByRole("dialog", { name: "Delete remote entry?", exact: true }).getByRole("button", { name: "Delete", exact: true }).click();
    await until(() => Promise.resolve(scenario.requests.some(item => item.method === "DELETE")), "confirmed delete");
    assert.equal(scenario.requests.find(item => item.action === "rename").body.toString(), JSON.stringify({ from: "/home/test/config.txt", to: "/home/test/中文.ini" }));
  });
  await test("binary upload keeps auth in headers and asks before replacing a remote file", async ({ page, goto }) => {
    await page.addInitScript(() => Object.defineProperty(globalThis.crypto, "subtle", { value: undefined }));
    const scenario = await fileScenario(page); await openFiles(page, goto);
    const payload = Buffer.from([0, 255, 7, 8]);
    await page.getByLabel("Upload file", { exact: true }).setInputFiles({ name: "config.txt", mimeType: "application/octet-stream", buffer: payload });
    await page.getByRole("dialog", { name: "Replace remote file?", exact: true }).waitFor();
    assert.equal(scenario.requests.filter(item => item.action === "start").length, 1);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await until(() => Promise.resolve(scenario.requests.filter(item => item.action === "complete").length === 1), "confirmed replacement");
    const uploads = scenario.requests.filter(item => item.action === "chunk");
    assert.deepEqual(uploads[0].body, payload);
    assert.ok(uploads.every(item => item.headers.authorization.startsWith("Bearer ")));
    assert.ok(uploads.every(item => !item.query.includes("token")));
  });
  await test("binary download saves the exact bytes", async ({ page, goto }) => {
    const { readFile } = await import("node:fs/promises");
    await fileScenario(page); await openFiles(page, goto);
    const waiting = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download config.txt", exact: true }).click();
    const download = await waiting;
    assert.deepEqual(await readFile(await download.path()), Buffer.from([0, 255, 1, 2]));
  });
  await test("cancel aborts an in-flight upload and restores file controls", async ({ page, goto }) => {
    const scenario = await fileScenario(page, { stall: true }); await openFiles(page, goto);
    await page.getByLabel("Upload file", { exact: true }).setInputFiles({ name: "pending.bin", mimeType: "application/octet-stream", buffer: Buffer.from("pending") });
    await until(() => Promise.resolve(scenario.pending.length === 1), "pending upload");
    await page.getByRole("button", { name: "Cancel transfer", exact: true }).click();
    await until(() => page.getByRole("button", { name: "New folder", exact: true }).isEnabled(), "controls restored");
    assert.equal(scenario.requests.filter(item => item.action === "chunk").length, 1);
  });
  await test("375px file view fits the screen and returns to the same terminal", async ({ page, goto }) => {
    await fileScenario(page); await openFiles(page, goto);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.equal(await surface(page).isVisible(), false);
    const box = await page.getByRole("button", { name: "Close files", exact: true }).boundingBox();
    assert.ok(box.width >= 44 && box.height >= 44);
    const count = await socketCount(page);
    await page.getByRole("button", { name: "Close files", exact: true }).click();
    assert.equal(await surface(page).isVisible(), true);
    assert.equal(await socketCount(page), count);
  }, { viewport: { width: 375, height: 812 }, mobile: true });
  await test("paused upload resumes from the server offset after page reload", async ({ page, goto }) => {
    const scenario = await fileScenario(page, { stall: true, stallChunk: 2 }); await openFiles(page, goto);
    const payload = Buffer.alloc(2 * 1024 * 1024 + 4, 19);
    await page.getByLabel("Upload file", { exact: true }).setInputFiles({ name: "resumable.bin", mimeType: "application/octet-stream", buffer: payload });
    await until(() => Promise.resolve(scenario.pending.length === 1), "second upload chunk stalled");
    await page.getByRole("button", { name: "Cancel transfer", exact: true }).click();
    await page.getByRole("button", { name: "Resume upload", exact: true }).waitFor();
    await page.reload();
    await page.getByRole("button", { name: "Files", exact: true }).click();
    await page.getByRole("button", { name: "Resume upload", exact: true }).waitFor();
    await page.getByLabel("Resume upload", { exact: true }).setInputFiles({ name: "resumable.bin", mimeType: "application/octet-stream", buffer: payload });
    await until(() => Promise.resolve(scenario.requests.some(item => item.action === "complete")), "resumed upload complete", 15_000);
    const offsets = scenario.requests.filter(item => item.action === "chunk").map(item => Number(new URLSearchParams(item.query).get("offset")));
    assert.deepEqual(offsets, [0, 1048576, 1048576, 2097152]);
    assert.equal(scenario.requests.filter(item => item.action === "start").length, 1);
  });
  await test("paused upload can be discarded without deleting the destination", async ({ page, goto }) => {
    const scenario = await fileScenario(page, { stall: true }); await openFiles(page, goto);
    await page.getByLabel("Upload file", { exact: true }).setInputFiles({ name: "partial.bin", mimeType: "application/octet-stream", buffer: Buffer.from("partial") });
    await until(() => Promise.resolve(scenario.pending.length === 1), "pending chunk");
    await page.getByRole("button", { name: "Cancel transfer", exact: true }).click();
    await page.getByRole("button", { name: "Discard partial upload", exact: true }).click();
    await until(() => Promise.resolve(scenario.requests.some(item => item.action === "uploads" && item.method === "DELETE")), "discarded upload");
    assert.equal(scenario.requests.filter(item => item.action === "entry").length, 0);
  });
  await test("text editor saves the displayed version and keeps draft on conflict", async ({ page, goto }) => {
    const scenario = await fileScenario(page, { conflictText: true }); await openFiles(page, goto);
    await page.getByRole("button", { name: "Edit text config.txt", exact: true }).click();
    const editor = page.getByRole("textbox", { name: "Edit text", exact: true });
    await editor.fill("my unsaved draft\n");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByText("The remote file changed. Your draft is kept. Reload the remote version before saving again.", { exact: true }).waitFor();
    assert.equal(await editor.inputValue(), "my unsaved draft\n");
    const save = scenario.requests.find(item => item.action === "text" && item.method === "PUT");
    assert.equal(JSON.parse(save.body).version, "sha256:initial");
    await page.getByRole("button", { name: "Reload remote file", exact: true }).click();
    await page.getByRole("dialog", { name: "Discard unsaved changes?", exact: true }).getByRole("button", { name: "Cancel", exact: true }).click();
    assert.equal(await editor.inputValue(), "my unsaved draft\n");
    await page.getByRole("button", { name: "Reload remote file", exact: true }).click();
    await page.getByRole("button", { name: "Discard changes", exact: true }).click();
    await until(async () => (await editor.inputValue()) === "external edit\n", "loaded latest remote text");
    await editor.fill("updated draft\n");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await until(() => Promise.resolve(scenario.requests.filter(item => item.action === "text" && item.method === "PUT").length === 2), "save after reload");
    const writes = scenario.requests.filter(item => item.action === "text" && item.method === "PUT");
    assert.equal(JSON.parse(writes[1].body).version, "sha256:external");
  });
  await test("directory upload preserves nested paths and directory download saves a ZIP", async ({ page, goto }) => {
    const { mkdtemp, mkdir, writeFile, rm, readFile } = await import("node:fs/promises");
    const folder = await mkdtemp("/tmp/talus-sftp-folder-");
    try {
      await mkdir(`${folder}/nested`); await writeFile(`${folder}/nested/中文.bin`, Buffer.from([0, 255, 1]));
      const scenario = await fileScenario(page); await openFiles(page, goto);
      await page.getByLabel("Upload folder", { exact: true }).setInputFiles(folder);
      await until(() => Promise.resolve(scenario.requests.some(item => item.action === "upload")), "folder file uploaded");
      const create = scenario.requests.find(item => item.action === "mkdir");
      assert.equal(JSON.parse(create.body).parents, true);
      const upload = scenario.requests.find(item => item.action === "upload");
      assert.ok(upload.path.endsWith("/nested/中文.bin")); assert.deepEqual(await page.evaluate(() => window.__binaryRequests.at(-1)), [0, 255, 1]);
      await until(() => page.getByRole("button", { name: "Download folder as ZIP folder ; $x", exact: true }).isEnabled(), "directory transfer settled");
      const waiting = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download folder as ZIP folder ; $x", exact: true }).click();
      const download = await waiting; assert.ok(download.suggestedFilename().endsWith(".zip"));
      assert.equal((await readFile(await download.path())).toString(), "mock ZIP bytes");
    } finally { await rm(folder, { recursive: true, force: true }); }
  });
  for (const changeDownload of [false, true]) {
    await test(`download resumes verified chunks${changeDownload ? " and replaces old chunks when the file changed" : ""}`, async ({ page, goto }) => {
      const { readFile } = await import("node:fs/promises");
      const scenario = await fileScenario(page, { largeDownload: true, changeDownload }); await openFiles(page, goto);
      await page.getByRole("button", { name: "Download config.txt", exact: true }).click();
      await until(() => Promise.resolve(scenario.pending.length === 1), "download second range stalled", 15_000);
      await page.getByRole("button", { name: "Cancel transfer", exact: true }).click();
      await page.getByRole("button", { name: "Resume download", exact: true }).waitFor();
      const waiting = page.waitForEvent("download");
      await page.getByRole("button", { name: "Resume download", exact: true }).click();
      const downloaded = await waiting;
      assert.deepEqual(await readFile(await downloaded.path()), scenario.downloadBytes());
      const calls = scenario.requests.filter(item => item.action === "download");
      assert.equal(calls.at(-1).headers.range, "bytes=16777216-17825791");
      assert.ok(calls.at(-1).headers["if-range"].startsWith('"sha256:'));
    });
  }

} finally {
  if (browser) await browser.close();
  server.kill();
  nameSuite("files", import.meta.url);
  writeReport({ passed, failed });
}
console.log(`SFTP UI: ${passed} passed, ${failed} failed`);
process.exitCode = failed > 0 ? 1 : 0;
