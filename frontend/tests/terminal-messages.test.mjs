import assert from "node:assert/strict";
import { nameSuite, recordCase, writeReport } from "./lib/case-report.mjs";
import { TERMINAL_MESSAGE_MAX_BYTES, terminalInputChunks } from "./.compiled-terminal-messages.mjs";

let failures = 0;
function check(name, fn) {
  try {
    fn();
    recordCase(name, true);
    console.log(`  ok: ${name}`);
  } catch (err) {
    failures++;
    recordCase(name, false);
    console.log(`  FAIL: ${name}\n    ${err.message}`);
  }
}

check("large pasted input preserves Unicode and escaped controls within the wire budget", () => {
  const encoder = new TextEncoder();
  const samples = [
    "x".repeat(200000),
    "中文🙂".repeat(30000),
    '\u0000\u0001\n\r\t"\\'.repeat(30000),
    "\ud800x\udc00".repeat(30000),
  ];
  for (const data of samples) {
    const chunks = [...terminalInputChunks(data)];
    assert.ok(chunks.length > 1);
    assert.equal(chunks.join(""), data);
    for (const chunk of chunks) {
      const serialized = JSON.stringify({ type: "input", data: chunk });
      assert.ok(encoder.encode(serialized).byteLength <= TERMINAL_MESSAGE_MAX_BYTES);
      assert.equal(JSON.parse(serialized).data, chunk);
    }
  }
});

check("small input keeps one message and empty input sends none", () => {
  assert.deepEqual([...terminalInputChunks("echo 中文🙂\n")], ["echo 中文🙂\n"]);
  assert.deepEqual([...terminalInputChunks("")], []);
});

check("an astral character at a chunk boundary is never split", () => {
  const overhead = new TextEncoder().encode(JSON.stringify({ type: "input", data: "" })).byteLength;
  const data = "x".repeat(TERMINAL_MESSAGE_MAX_BYTES - overhead - 2) + "🙂tail";
  const chunks = [...terminalInputChunks(data)];
  assert.equal(chunks.length, 2);
  assert.equal(chunks.join(""), data);
  assert.equal(chunks[1], "🙂tail");
});

nameSuite("terminal-messages", import.meta.url);
writeReport();
process.exit(failures === 0 ? 0 : 1);
