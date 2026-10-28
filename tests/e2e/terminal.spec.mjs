// M4 end-to-end: the built SPA, served by a real Hub, drives a real SSH terminal to
// a controlled peer. This is the browser flow the plan requires on top of the
// protocol-level terminal-e2e suite.
//
// Prerequisites come from tests/e2e/run.sh:
//   HUB_URL      the running Hub (serving both the API and the built SPA)
//   BOOTSTRAP_TOKEN the owned fixture setup proof, separate from account credentials
//   PEER_LOG     JSONL events recorded by cmd/e2efixture (the SSH peer)
//   CLIENT_KEY   the private key whose public half the peer accepts
//   PEER_HOST_PUBLIC_KEY the controlled peer's public host key for verification
//
// The peer echoes terminal input and records PTY requests, window changes and
// disconnects, so every assertion below is about the real browser -> WS -> SSH -> PTY
// path rather than a mocked one.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

import { chromium } from "playwright";

const hubUrl = process.env.HUB_URL;
const peerLog = process.env.PEER_LOG;
const clientKey = process.env.CLIENT_KEY;
const peerHostKey = process.env.PEER_HOST_PUBLIC_KEY;
const bootstrapToken = process.env.BOOTSTRAP_TOKEN;
const username = process.env.E2E_USERNAME ?? "e2e-admin";
const password = process.env.E2E_PASSWORD ?? "e2e-password-1234";

if (!hubUrl || !peerLog || !clientKey || !peerHostKey || !bootstrapToken) {
	console.error("terminal.spec.mjs requires HUB_URL, PEER_LOG, CLIENT_KEY, PEER_HOST_PUBLIC_KEY and BOOTSTRAP_TOKEN");
	process.exit(2);
}

const events = async () => {
	const raw = await readFile(peerLog, "utf8").catch(() => "");
	return raw
		.split("\n")
		.filter((line) => line.trim() !== "")
		.map((line) => JSON.parse(line));
};

/** Everything the peer received on its terminal sessions, concatenated: xterm sends
 * one frame per keystroke, so a whole command only exists in the joined stream. */
const typedText = async () =>
	(await events())
		.filter((event) => event.type === "input")
		.map((event) => event.data)
		.join("");

/** The peer's event count now: a cursor proves an event is new, not historical. */
const cursor = async () => (await events()).length;

/** The most recent shell channel the peer accepted, with its connection id. */
async function currentShell() {
	const shells = (await events()).filter((event) => event.type === "shell");
	const shell = shells.at(-1);
	return shell
		? { session: shell.session, channel: shell.channel, index: shells.length }
		: null;
}

/** Waits until *this* channel's shell is closed, not just any end event (F13). */
async function untilShellClosed(shell, fromCursor, description) {
	const deadline = Date.now() + 15000;
	while (Date.now() < deadline) {
		const log = await events();
		const closed = log.findIndex(
			(event, index) =>
				index >= fromCursor &&
				event.type === "shell-closed" &&
				event.channel === shell.channel &&
				event.session === shell.session,
		);
		if (closed >= 0) return;
		await new Promise((resolve) => setTimeout(resolve, 100));
	}
	const seen = (await events())
		.slice(fromCursor)
		.map((event) => `${event.type}:${event.channel ?? "-"}`)
		.join(",");
	throw new Error(`Timed out waiting for ${description} (new events: ${seen})`);
}

/** Waits until the peer log contains an event matching the predicate. */
async function untilPeer(predicate, description, timeout = 15000) {
	const deadline = Date.now() + timeout;
	while (Date.now() < deadline) {
		if ((await events()).some(predicate)) return;
		await new Promise((resolve) => setTimeout(resolve, 100));
	}
	const seen = (await events()).map((event) => event.type).join(",");
	throw new Error(
		`Timed out waiting for ${description} in the peer log (saw: ${seen})`,
	);
}

async function until(check, description, timeout = 15000) {
	const deadline = Date.now() + timeout;
	while (Date.now() < deadline) {
		if (await check()) return;
		await new Promise((resolve) => setTimeout(resolve, 100));
	}
	throw new Error(`Timed out: ${description}`);
}

/** An API call authenticated with the token the browser obtained. */
async function api(token, path, body) {
	const response = await fetch(`${hubUrl}/api/v1${path}`, {
		method: body ? "POST" : "GET",
		headers: {
			"Content-Type": "application/json",
			Authorization: `Bearer ${token}`,
		},
		body: body ? JSON.stringify(body) : undefined,
	});
	const payload = await response.json().catch(() => ({}));
	if (!response.ok) {
		throw new Error(
			`${path} failed: ${response.status} ${JSON.stringify(payload)}`,
		);
	}
	return payload.data ?? payload;
}

const browser = await chromium.launch({
	headless: true,
	executablePath: process.env.CHROMIUM_EXECUTABLE || undefined,
	args: JSON.parse(process.env.CHROMIUM_ARGS ?? "[]"),
});
const context = await browser.newContext({
	viewport: { width: 1280, height: 900 },
	locale: "en-US",
});
await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: hubUrl });
const page = await context.newPage();
page.setDefaultTimeout(20000);
const pageErrors = [];
const requestUrls = [];
page.on("pageerror", (error) => pageErrors.push(error.message));
page.on("request", (request) => requestUrls.push(request.url()));

const results = [];
const step = async (name, run) => {
	await run();
	results.push(name);
	console.log(`ok: ${name}`);
};

try {
	await step(
		"a fresh Hub walks the browser through first-admin setup and sign-in",
		async () => {
			await page.goto(`${hubUrl}/login`);
			// A database without users redirects the SPA to /setup: the run exercises the
			// real bootstrap path instead of seeding a user behind the UI's back.
			await page.waitForURL((url) => url.pathname !== "/login", {
				timeout: 20000,
			});
			assert.equal(new URL(page.url()).pathname, "/setup", "the owned fresh database must require setup");
			// Ordinary login must not claim the first administrator, and possession
			// of only the proposed account password must not satisfy ownership.
			for (const [path, body, status] of [
				["login", { username, password }, 401],
				["setup", { username, password, token: "incorrect-fixture-proof-0123456789" }, 403],
			]) {
				const response = await fetch(`${hubUrl}/api/v1/auth/${path}`, {
					method: "POST",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify(body),
				});
				assert.equal(response.status, status, "unproved first-admin setup must fail");
				const rejected = await response.text();
				assert.ok(!rejected.includes(bootstrapToken), "the proof must not appear in an error response");
			}
			await page.getByLabel("Setup token", { exact: true }).fill(bootstrapToken);
			await page.getByLabel("Username", { exact: true }).fill(username);
			await page.getByLabel("Password", { exact: true }).fill(password);
			await page.getByLabel("Confirm Password", { exact: true }).fill(password);
			await page.getByRole("button", { name: /create admin account/i }).click();
			// The dashboard is only reachable with a session, so this proves the JWT works.
			await until(
				async () =>
					!new URL(page.url()).pathname.startsWith("/setup") &&
					!new URL(page.url()).pathname.startsWith("/login"),
				"the dashboard to load after authentication",
			);
		},
	);

	const token = await page.evaluate(() => localStorage.getItem("auth_token"));
	assert.ok(token && token.length > 20, "the browser stored a JWT");
	const persistedBrowserState = await page.evaluate(() => JSON.stringify({
		local: { ...localStorage }, session: { ...sessionStorage },
	}));
	assert.ok(!persistedBrowserState.includes(bootstrapToken), "the setup proof must not be persisted in browser storage");
	const repeatedSetup = await fetch(`${hubUrl}/api/v1/auth/setup`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ token: bootstrapToken, username, password }),
	});
	assert.equal(repeatedSetup.status, 409, "initialized setup must remain closed even with the correct proof");

	// The terminal needs a target: a credential whose private key the peer accepts and
	// a server row pointing at the peer's port.
	const peerPort = Number(process.env.PEER_PORT);
	let serverId = Number(process.env.SERVER_ID ?? 0);
	let createdServer = false;
	await step(
		"a server is registered against the peer with a key credential",
		async () => {
			if (serverId === 0) {
				const privateKey = await readFile(clientKey, "utf8");
				const credential = await api(token, "/credentials", {
					name: "e2e peer key",
					auth_type: "private_key",
					username: "e2e",
					private_key: privateKey,
				});
				const server = await api(token, "/servers", {
					name: "e2e peer",
					host: "127.0.0.1",
					port: peerPort,
					credential_id: credential.id,
				});
				serverId = server.id;
				createdServer = true;
			}
			assert.ok(serverId > 0, "a server id is available");
		},
	);

	await page.goto(`${hubUrl}/servers/${serverId}/terminal`);
	if (createdServer) {
		await step("the first host fingerprint matches the controlled peer before explicit trust", async () => {
			const publicKey = (await readFile(peerHostKey, "utf8")).trim().split(/\s+/)[1];
			const fingerprint = `SHA256:${createHash("sha256").update(Buffer.from(publicKey, "base64")).digest("base64").replace(/=+$/, "")}`;
			const pending = page.getByTestId("pending-host-key");
			await pending.waitFor();
			assert.equal(await pending.locator("dd").innerText(), fingerprint);
			assert.equal(await page.locator("[data-connection-status]").count(), 0, "the terminal stays blocked before trust");
			assert.equal((await events()).some(event => event.type === "shell"), false, "no shell is opened before trust");
			await page.getByRole("button", { name: "I verified this fingerprint — trust host", exact: true }).click();
			await pending.waitFor({ state: "hidden" });
		});
	}

	let firstShell = null;
	await step("the terminal connects through a real SSH PTY", async () => {
		await until(
			async () =>
				(await page
					.locator("[data-connection-status]")
					.getAttribute("data-connection-status")) === "connected",
			"the terminal to report connected",
		);
		await untilPeer(
			(event) => event.type === "shell",
			"the peer to enter a shell",
		);
		firstShell = await currentShell();
		// "Connected" alone proves nothing about the PTY: the shell channel must have
		// carried a valid pty-req with a real size (F14).
		await untilPeer(
			(event) =>
				event.type === "pty-req" &&
				event.channel === firstShell.channel &&
				event.size?.[0] > 0 &&
				event.size?.[1] > 0,
			`a valid pty-req on ${firstShell?.channel}`,
		);
		await untilPeer(
			(event) => event.type === "pty-req-invalid",
			`no malformed pty-req (should not appear)`,
			1500,
		).then(
			() => {
				throw new Error("the Hub sent a malformed pty-req");
			},
			() => {},
		);
		// The token is sent in the first WS frame, never in a URL (REQ-09 生命周期).
		assert.deepEqual(
			requestUrls.filter(
				(url) => url.includes("token=") || url.includes("auth_token"),
			),
			[],
			"no request carries the token in its URL",
		);
	});

	await step(
		"typing reaches the PTY and the output returns to the browser",
		async () => {
			await page.getByTestId("terminal-surface").click();
			await page.keyboard.type("echo e2e-marker-one\r");
			await until(
				async () => (await typedText()).includes("e2e-marker-one"),
				"the peer to receive the typed command",
			);
			await until(
				async () =>
					(await page.getByTestId("terminal-surface").innerText()).includes(
						"e2e-marker-one",
					),
				"the echoed output to render in the browser",
			);
		},
	);

	await step(
		"a large Unicode paste preserves every byte over the real terminal connection",
		async () => {
			const before = await cursor();
			// Over64KiB before JSON escaping: Chinese, surrogate pairs, quotes,
			// backslashes and tabs exercise both Unicode boundaries and wire size.
			const payload = 'large-paste-start|' + '中😀"\\\t'.repeat(8000) + '|large-paste-end';
			const expected = Buffer.from(payload, "utf8");
			assert.ok(expected.length > (64 << 10) && expected.length < (128 << 10));
			const received = async () => Buffer.concat((await events()).slice(before)
				.filter((event) => event.type === "input" && event.channel === firstShell.channel && event.session === firstShell.session)
				.map((event) => Buffer.from(event.data_base64 ?? "", "base64")));
			await page.evaluate((text) => navigator.clipboard.writeText(text), payload);
			await page.getByRole("button", { name: "Paste from clipboard" }).click();
			await until(async () => (await received()).length >= expected.length,
				"the controlled SSH peer to receive the complete large paste");
			const actual = await received();
			assert.equal(actual.length, expected.length, "the paste must arrive once without truncation");
			assert.ok(actual.equals(expected), "the SSH peer must receive the exact Unicode byte sequence in order");
			assert.equal(await page.locator("[data-connection-status]").getAttribute("data-connection-status"), "connected");
		},
	);

	await step(
		"a layout change resizes the PTY over the live session",
		async () => {
			const before = (await events()).filter(
				(event) => event.type === "window-change",
			).length;
			await page.setViewportSize({ width: 900, height: 700 });
			await until(
				async () =>
					(await events()).filter((event) => event.type === "window-change")
						.length > before,
				"a window-change at the peer",
			);
			const change = (await events())
				.filter((event) => event.type === "window-change")
				.at(-1);
			assert.ok(
				change.size[0] > 0 && change.size[1] > 0,
				`window-change carried a size: ${JSON.stringify(change)}`,
			);
			// It must be *this* terminal's channel, not another channel on the connection.
			assert.equal(
				change.channel,
				firstShell.channel,
				"the resize belongs to the terminal channel",
			);
		},
	);

	await step("disconnect and reconnect start a new session", async () => {
		const beforeDisconnect = await cursor();
		await page.getByRole("button", { name: "Disconnect" }).click();
		// The *terminal's* channel must close; a connection-level event or another
		// channel ending is not evidence of reclaiming this terminal (F13).
		await untilShellClosed(
			firstShell,
			beforeDisconnect,
			"the terminal channel to close",
		);
		await until(
			async () =>
				(await page
					.locator("[data-connection-status]")
					.getAttribute("data-connection-status")) === "disconnected",
			"the toolbar to report disconnected",
		);

		await page.getByRole("button", { name: "Connect" }).click();
		await until(
			async () =>
				(await events()).filter((event) => event.type === "shell").length > firstShell.index,
			"a new shell after reconnect",
		);
		const secondShell = await currentShell();
		assert.notEqual(
			secondShell.channel,
			firstShell.channel,
			"the reconnect opened its own channel",
		);
		firstShell = secondShell;
		await page.getByTestId("terminal-surface").click();
		await page.keyboard.type("echo e2e-marker-two\r");
		await until(
			async () => (await typedText()).includes("e2e-marker-two"),
			"the second session to carry the typed command",
		);
		await until(
			async () =>
				(await page.getByTestId("terminal-surface").innerText()).includes(
					"e2e-marker-two",
				),
			"the second session's output to render",
		);
	});

	await step("leaving the terminal reclaims the session", async () => {
		const before = await cursor();
		const shell = firstShell;
		await page.getByRole("link", { name: "Back to server" }).click();
		await until(
			async () => (await page.getByTestId("terminal-surface").count()) === 0,
			"the terminal to unmount",
		);
		// Only the channel this terminal opened counts as reclamation (F13).
		await untilShellClosed(
			shell,
			before,
			"the unmounted terminal's channel to close",
		);
	});

	assert.deepEqual(pageErrors, [], "no uncaught page errors");
	console.log(`terminal e2e: ${results.length} steps passed`);
} catch (error) {
	console.error(`terminal e2e FAILED: ${error.stack}`);
	process.exitCode = 1;
} finally {
	await context.close();
	await browser.close();
}
