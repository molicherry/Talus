// Tests for the terminal toolbar's pure logic: display preferences, paste
// classification and the connection-state model. These pin the parts of TC-09-01
// (toolbar controls, font sizing, clipboard safety) and TC-09-02 (375px/landscape
// surface, reconnect semantics) that do not need a browser.

import assert from "node:assert/strict";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";

import {
  TERMINAL_FONT_SIZE_DEFAULT,
  TERMINAL_FONT_SIZE_KEY,
  TERMINAL_FONT_SIZE_MAX,
  TERMINAL_FONT_SIZE_MIN,
  clampFontSize,
  loadFontSize,
  saveFontSize,
  stepFontSize,
} from "./.compiled-terminal-preferences.mjs";
import { TERMINAL_SHORTCUTS, classifyPaste } from "./.compiled-terminal-input.mjs";
import { describeConnection } from "./.compiled-connection-status.mjs";

let failures = 0;
function check(name, fn) {
  recordCase(name);
  try {
    fn();
    console.log(`  ok: ${name}`);
  } catch (err) {
    failures++;
    console.log(`  FAIL: ${name}\n    ${err.message}`);
  }
}

/** A storage double that records what the module wrote. */
function store(initial = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (key) => (key in data ? data[key] : null),
    setItem: (key, value) => {
      data[key] = String(value);
    },
  };
}

check("font size is clamped into the usable range", () => {
  assert.equal(clampFontSize(TERMINAL_FONT_SIZE_DEFAULT), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(clampFontSize(TERMINAL_FONT_SIZE_MIN - 5), TERMINAL_FONT_SIZE_MIN);
  assert.equal(clampFontSize(TERMINAL_FONT_SIZE_MAX + 5), TERMINAL_FONT_SIZE_MAX);
  assert.equal(clampFontSize(13.4), 13);
  assert.equal(clampFontSize(Number.NaN), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(clampFontSize(Number.POSITIVE_INFINITY), TERMINAL_FONT_SIZE_DEFAULT);
});

check("stepping the font size stops at the ends", () => {
  assert.equal(stepFontSize(14, 1), 15);
  assert.equal(stepFontSize(14, -1), 13);
  assert.equal(stepFontSize(TERMINAL_FONT_SIZE_MAX, 1), TERMINAL_FONT_SIZE_MAX);
  assert.equal(stepFontSize(TERMINAL_FONT_SIZE_MIN, -1), TERMINAL_FONT_SIZE_MIN);
});

check("the font size is remembered and survives a reload", () => {
  const storage = store();
  assert.equal(loadFontSize(storage), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(saveFontSize(18, storage), 18);
  assert.equal(storage.data[TERMINAL_FONT_SIZE_KEY], "18");
  assert.equal(loadFontSize(storage), 18);
});

check("a corrupt or out-of-range stored size cannot break the terminal", () => {
  assert.equal(loadFontSize(store({ [TERMINAL_FONT_SIZE_KEY]: "huge" })), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(loadFontSize(store({ [TERMINAL_FONT_SIZE_KEY]: "" })), TERMINAL_FONT_SIZE_DEFAULT);
  // Clamped rather than rejected, then persisted in its clamped form.
  assert.equal(loadFontSize(store({ [TERMINAL_FONT_SIZE_KEY]: "99" })), TERMINAL_FONT_SIZE_MAX);
  const clamped = store({ [TERMINAL_FONT_SIZE_KEY]: "99" });
  assert.equal(saveFontSize(loadFontSize(clamped), clamped), TERMINAL_FONT_SIZE_MAX);
});

check("storage failures are ignored, not fatal", () => {
  const hostile = {
    getItem() {
      throw new Error("blocked");
    },
    setItem() {
      throw new Error("blocked");
    },
  };
  assert.equal(loadFontSize(hostile), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(saveFontSize(16, hostile), 16);
  assert.equal(loadFontSize(null), TERMINAL_FONT_SIZE_DEFAULT);
  assert.equal(saveFontSize(16, null), 16);
});

check("a single-line paste needs no confirmation", () => {
  const shape = classifyPaste("ls -la");
  assert.equal(shape.kind, "single-line");
  assert.equal(shape.lines, 1);
  assert.equal(shape.preview, "ls -la");
  assert.equal(shape.bytes, 6);
});

check("a multi-line paste is described before it is sent", () => {
  const shape = classifyPaste("rm -rf /tmp/a\nrm -rf /tmp/b\n");
  assert.equal(shape.kind, "multi-line");
  // A single trailing newline does not add an empty command.
  assert.equal(shape.lines, 2);
  assert.equal(shape.preview, "rm -rf /tmp/a");
});

check("CRLF and CR clipboards are counted as line breaks too", () => {
  assert.equal(classifyPaste("a\r\nb").kind, "multi-line");
  assert.equal(classifyPaste("a\r\nb").lines, 2);
  assert.equal(classifyPaste("a\rb").lines, 2);
});

check("an empty clipboard is reported as empty", () => {
  assert.equal(classifyPaste("").kind, "empty");
  assert.equal(classifyPaste("").lines, 0);
  assert.equal(classifyPaste("").bytes, 0);
  // A lone newline is one line the user asked to send (an empty command), so it is
  // pasted rather than reported as an empty clipboard.
  assert.equal(classifyPaste("\n").kind, "single-line");
  assert.equal(classifyPaste("\n").lines, 1);
  assert.equal(classifyPaste("\n").bytes, 1);
});

check("the paste preview is capped", () => {
  const long = "x".repeat(200);
  const shape = classifyPaste(long);
  assert.ok(shape.preview.length <= 61, `preview length ${shape.preview.length}`);
  assert.ok(shape.preview.endsWith("…"));
  assert.equal(shape.bytes, 200);
});

check("the advertised shortcuts cover copy, paste, search and clear", () => {
  const keys = TERMINAL_SHORTCUTS.map((hint) => hint.keys);
  for (const want of ["Ctrl+Shift+C", "Ctrl+Shift+V", "Ctrl+F", "Esc", "Ctrl+Shift+L"]) {
    assert.ok(keys.includes(want), `missing shortcut ${want}`);
  }
  // Ctrl+C and Ctrl+L must stay available to the remote shell as interrupt and
  // clear-screen; the local clear therefore uses Ctrl+Shift+L.
  assert.ok(!keys.includes("Ctrl+C"));
  assert.ok(!keys.includes("Ctrl+L"));
});

check("each connection state is distinct and actionable", () => {
  const connecting = describeConnection({ status: "connecting", retryCount: 0, maxRetries: 5, error: null });
  assert.equal(connecting.labelKey, "terminal.connecting");
  assert.equal(connecting.canConnect, false);
  assert.equal(connecting.canDisconnect, false);
  // A connect that hangs can be cancelled.
  assert.equal(connecting.canCancelRetry, true);

  const connected = describeConnection({ status: "connected", retryCount: 0, maxRetries: 5, error: null });
  assert.equal(connected.canDisconnect, true);
  assert.equal(connected.canConnect, false);

  const disconnected = describeConnection({ status: "disconnected", retryCount: 0, maxRetries: 5, error: null });
  assert.equal(disconnected.labelKey, "terminal.disconnected");
  assert.equal(disconnected.canConnect, true);
  assert.equal(disconnected.isRetry, false);

  const failed = describeConnection({ status: "disconnected", retryCount: 3, maxRetries: 5, error: "boom" });
  assert.equal(failed.labelKey, "terminal.failed");
  assert.equal(failed.tone, "error");
  assert.equal(failed.canConnect, true);
  assert.equal(failed.isRetry, true);
});

check("a reconnect announces a new session, never a restored one", () => {
  const retrying = describeConnection({
    status: "reconnecting",
    retryCount: 2,
    maxRetries: 5,
    error: null,
    retryPending: true,
  });
  assert.equal(retrying.isNewSession, true);
  assert.equal(retrying.labelKey, "terminal.reconnecting");
  assert.deepEqual(retrying.vars, { attempt: 2, max: 5 });
  // A retry is cancellable while its timer waits and while its socket is in flight.
  assert.equal(retrying.canCancelRetry, true);
  const inFlight = describeConnection({
    status: "reconnecting",
    retryCount: 3,
    maxRetries: 5,
    error: null,
    retryPending: false,
  });
  // The count and the new-session hint survive the transition from waiting to
  // connecting (F08).
  assert.deepEqual(inFlight.vars, { attempt: 3, max: 5 });
  assert.equal(inFlight.isNewSession, true);
  assert.equal(inFlight.canCancelRetry, true);
  // Manual reconnect after a failure is still a new session.
  const manual = describeConnection({ status: "disconnected", retryCount: 5, maxRetries: 5, error: "max" });
  assert.equal(manual.isNewSession, true);
});

console.log(
  failures === 0
    ? "\nterminal-controls: ALL PASS"
    : `\nterminal-controls: ${failures} FAILURES`,
);
nameSuite("terminal-controls");
writeReport();

process.exit(failures === 0 ? 0 : 1);

