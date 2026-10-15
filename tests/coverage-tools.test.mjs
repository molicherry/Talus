import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readdirSync, readFileSync, writeFileSync, copyFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { inspectGoResults } from "./go-results.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "talus-coverage-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "tests"));
  mkdirSync(join(root, "backend"));
  mkdirSync(join(root, "bin"));
  for (const file of ["run-suites.mjs", "render-coverage.mjs", "go-results.mjs"]) {
    copyFileSync(join(HERE, file), join(root, "tests", file));
  }
  writeFileSync(join(root, "backend", "go.mod"), "module example.test/backend\n\ngo 1.25\n");
  writeFileSync(join(root, "backend", "acceptance_test.go"), "package backend\nfunc TestRequired(t *testing.T) {}\n");
  // The fixture isolates dispatcher behavior from the installed toolchain;
  // production tests still run native Go and report real -json events.
  writeFileSync(join(root, "bin", "go"), `#!${process.execPath}\nprocess.stdout.write(process.env.FIXTURE_GO_EVENTS || "");\nprocess.exitCode = Number(process.env.FIXTURE_GO_EXIT || 0);\n`, { mode: 0o755 });
  const doc = {
    schema_version: 1,
    runs: [{ run_id: "native-go", suite: "integration", runner: "go-test", cwd: "backend", command: ["go", "test", "./..."], timeout_seconds: 10, environment: { profile: "fixture", requires: [] } }],
    gates: { M2: ["native-go"] },
    phases: { M2: ["REQ-05"] },
    cases: [{ case_id: "TC-05-05", requirement_ids: ["REQ-05"], spec_ref: "fixture", scenario: "Reject missing acceptance evidence before passing a required gate.", implementations: [{ implementation_id: "required-test", run_id: "native-go", implementation: { path: "backend/acceptance_test.go", symbol: "TestRequired" }, automation_status: "implemented", required: true }] }],
    scan_exemptions: [],
  };
  return {
    doc,
    run(events = [], extraEnv = {}) {
      writeFileSync(join(root, "tests", "coverage.json"), JSON.stringify(doc));
      return spawnSync(process.execPath, [join(root, "tests", "run-suites.mjs"), "--gate", "M2"], {
        cwd: root,
        encoding: "utf8",
        env: { ...process.env, PATH: `${join(root, "bin")}:${process.env.PATH}`, FIXTURE_GO_EVENTS: events.map((event) => JSON.stringify(event)).join("\n"), ...extraEnv },
      });
    },
  };
}


// A node-native run: the fake `node` writes structured reports (what
// --test-reporter does in a real run) and can also print misleading stdout.
function nodeFixture(t) {
  const root = mkdtempSync(join(tmpdir(), "talus-node-"));
  if (!process.env.TALUS_KEEP_FIXTURE) {
    t.after(() => rmSync(root, { recursive: true, force: true }));
  } else {
    console.log("fixture root:", root);
  }
  mkdirSync(join(root, "tests"));
  mkdirSync(join(root, "bin"));
  for (const file of ["run-suites.mjs", "render-coverage.mjs", "go-results.mjs"]) {
    copyFileSync(join(HERE, file), join(root, "tests", file));
  }
  // The registered implementation file must exist: the runner validates that the
  // manifest points at real paths. The fake node never executes them.
  mkdirSync(join(root, "required"));
  mkdirSync(join(root, "other"));
  writeFileSync(join(root, "required", "acceptance.test.mjs"), "// registered source\n");
  writeFileSync(join(root, "other", "acceptance.test.mjs"), "// same basename, different file\n");
  // Reports are pre-rendered here and copied in by the fake node with the run id the
  // runner generated, so nothing has to spawn another interpreter.
  mkdirSync(join(root, "fixture-reports"));
  writeFileSync(join(root, "bin", "node"), `#!/bin/sh
if [ -d "$FIXTURE_REPORT_DIR" ]; then
  mkdir -p "$TALUS_UI_REPORT_DIR"
  for report in "$FIXTURE_REPORT_DIR"/*.json; do
    [ -e "$report" ] || continue
    sed "s/__RUN_ID__/$TALUS_UI_RUN_ID/" "$report" > "$TALUS_UI_REPORT_DIR/ui-$(basename "$report")"
  done
fi
printf '%s' "$FIXTURE_NODE_STDOUT"
exit 0
`, { mode: 0o755 });
  const doc = {
    schema_version: 1,
    runs: [{ run_id: "node-native", suite: "fast", runner: "node-test", cwd: ".", command: ["node", "--test", "required/acceptance.test.mjs", "other/acceptance.test.mjs"], timeout_seconds: 10, environment: { profile: "fixture", requires: [] } }],
    gates: { M4: ["node-native"] },
    phases: { M4: ["REQ-09"] },
    cases: [{ case_id: "TC-09-01", requirement_ids: ["REQ-09"], spec_ref: "REQ §9", scenario: "Only a passing test from the registered implementation file satisfies a required case.", implementations: [{ implementation_id: "required-node", run_id: "node-native", implementation: { path: "required/acceptance.test.mjs", symbol: "Required current terminal check" }, automation_status: "implemented", required: true }] }],
    scan_exemptions: [],
  };
  return {
    run(reports = [], stdout = "", env = {}) {
      writeFileSync(join(root, "tests", "coverage.json"), JSON.stringify(doc));
      // One pre-rendered report per entry; __RUN_ID__ is filled in by the fake node.
      for (const file of readdirSync(join(root, "fixture-reports"))) {
        rmSync(join(root, "fixture-reports", file), { force: true });
      }
      reports.forEach((report, index) => {
        writeFileSync(
          join(root, "fixture-reports", `${index}-${report.suite}.json`),
          JSON.stringify({
            ...report,
            run_id: "__RUN_ID__",
            // The matcher requires evidence with at least one executed case.
            executed: report.cases.length,
            failed: report.cases.filter((entry) => !entry.ok).length,
          }),
        );
      });
      return spawnSync(process.execPath, [join(root, "tests", "run-suites.mjs"), "--gate", "M4"], {
        cwd: root,
        encoding: "utf8",
        env: {
          ...process.env,
          PATH: `${join(root, "bin")}:${process.env.PATH}`,
          FIXTURE_REPORT_DIR: join(root, "fixture-reports"),
          FIXTURE_NODE_STDOUT: stdout,
          ...env,
        },
      });
    },
  };
}

test("the generated report matches the committed machine inventory", () => {
  const result = spawnSync(process.execPath, [join(HERE, "render-coverage.mjs"), "--check"], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test("a required planned implementation blocks its phase before execution", (t) => {
  const f = fixture(t);
  f.doc.cases[0].implementations[0].automation_status = "planned";
  f.doc.cases[0].implementations[0].implementation = null;
  const result = f.run();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /unimplemented required cases/);
  assert.doesNotMatch(result.stdout, /selected /);
});

test("removing a required run from a gate cannot hide unfinished coverage", (t) => {
  const f = fixture(t);
  f.doc.runs.push({ ...f.doc.runs[0], run_id: "unrelated" });
  f.doc.gates.M2 = ["unrelated"];
  const result = f.run();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /does not include runs covering required cases/);
});

test("a missing implementation symbol fails schema validation", (t) => {
  const f = fixture(t);
  f.doc.cases[0].implementations[0].implementation.symbol = "TestNonexistent";
  const result = f.run();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /test symbol TestNonexistent does not exist/);
});

test("an exit-zero Go runner cannot pass when its filter selects no required test", (t) => {
  const f = fixture(t);
  const result = f.run([{ Package: "example.test/backend", Action: "pass" }]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /TestRequired \(not_run\)/);
});

test("a skipped required Go test is not accepted as passing", (t) => {
  const f = fixture(t);
  const result = f.run([{ Package: "example.test/backend", Test: "TestRequired", Action: "skip" }]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /TestRequired \(skip\)/);
});

test("a required Go test passes only with evidence from its own package", (t) => {
  const f = fixture(t);
  const wrong = f.run([{ Package: "example.test/other", Test: "TestRequired", Action: "pass" }]);
  assert.equal(wrong.status, 1);
  const correct = f.run([{ Package: "example.test/backend", Test: "TestRequired", Action: "pass" }]);
  assert.equal(correct.status, 0, correct.stderr);
});

test("a skipped subscenario cannot hide under a passing Go parent", () => {
  const expected = [{ package: "example.test/backend", test: "TestRequired" }];
  const output = [
    { Package: "example.test/backend", Test: "TestRequired/database", Action: "skip" },
    { Package: "example.test/backend", Test: "TestRequired", Action: "pass" },
  ].map((event) => JSON.stringify(event)).join("\n");
  assert.equal(inspectGoResults(output, expected)[0].status, "skip");
});

test("a missing run dependency blocks execution rather than skipping tests", (t) => {
  const f = fixture(t);
  f.doc.runs[0].environment.requires = ["FIXTURE_REQUIRED_DATABASE"];
  const result = f.run([], { FIXTURE_REQUIRED_DATABASE: "" });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /requires env FIXTURE_REQUIRED_DATABASE/);
  assert.doesNotMatch(result.stdout, /▶ native-go/);
});

test("a nonzero native runner fails even after emitting a required test pass", (t) => {
  const f = fixture(t);
  const result = f.run([{ Package: "example.test/backend", Test: "TestRequired", Action: "pass" }], { FIXTURE_GO_EXIT: "7" });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /native-go: exit 7/);
});

test("required manual verification remains pending without recorded evidence", (t) => {
  const f = fixture(t);
  f.doc.runs[0].runner = "manual";
  f.doc.runs[0].command = null;
  f.doc.runs[0].timeout_seconds = null;
  f.doc.cases[0].implementations[0].automation_status = "manual";
  f.doc.cases[0].implementations[0].implementation = null;
  const result = f.run();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /manual run native-go: pending \(missing tests\/evidence\/native-go.json\)/);
});

test("a duplicate implementation identifier fails schema validation", (t) => {
  const f = fixture(t);
  f.doc.cases[0].implementations.push({ ...f.doc.cases[0].implementations[0] });
  const result = f.run();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /duplicate or missing implementation_id required-test/);
});

test("a passing test from another directory cannot satisfy a same-named implementation", (t) => {
  // Same basename, different directory, and the run's stdout even claims a pass for
  // the registered name (S03 counterexample A).
  const f = nodeFixture(t);
  const other = [{ suite: "node-native", file: "other/acceptance.test.mjs", cases: [{ name: "Required current terminal check", ok: true }] }];
  const result = f.run(other, "\u2714 Required current terminal check (1ms)\n");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Required current terminal check \(not_run\)/);
});

test("a skipped required test cannot borrow a same-named pass from another file", (t) => {
  // Both files are selected: the registered one skips, the other passes with the same
  // name (S03 counterexample B).
  const f = nodeFixture(t);
  const reports = [
    { suite: "node-native-required", file: "required/acceptance.test.mjs", cases: [{ name: "Required current terminal check", ok: false }] },
    { suite: "node-native-other", file: "other/acceptance.test.mjs", cases: [{ name: "Required current terminal check", ok: true }] },
  ];
  const result = f.run(reports, "- Required current terminal check # SKIP\n\u2714 Required current terminal check (1ms)\n");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Required current terminal check \(not_run\)/);
});

test("a passing test from the registered file satisfies its implementation", (t) => {
  const f = nodeFixture(t);
  const reports = [
    { suite: "node-native-required", file: "required/acceptance.test.mjs", cases: [{ name: "Required current terminal check", ok: true }] },
  ];
  const result = f.run(reports, "\u2714 Required current terminal check (1ms)\n");
  assert.equal(result.status, 0, result.stderr);
});

test("the native reporter preserves assertion diagnostics and structured outcomes", (t) => {
  const root = mkdtempSync(join(tmpdir(), "talus-node-diagnostics-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const reportDir = join(root, "reports");
  writeFileSync(join(root, "diagnostics.test.mjs"), `
import test from "node:test";
import assert from "node:assert/strict";
test("a passing native test", () => {});
test.skip("a skipped native test", () => {});
test.todo("a todo native test");
test("a failing native assertion", (t) => {
  console.log("native stdout remains visible");
  console.error("native stderr remains visible");
  t.diagnostic("native diagnostic remains visible");
  assert.equal("actual-value", "expected-value", "native assertion details remain visible");
});
`);
  const env = { ...process.env, NODE_DISABLE_COLORS: "1", FORCE_COLOR: "0", TALUS_UI_REPORT_DIR: reportDir, TALUS_UI_RUN_ID: "native-diagnostics", TALUS_UI_SUITE: "native-diagnostics" };
  // This is a separate native test runner, not a child test of this process.
  delete env.NODE_TEST_CONTEXT;
  const result = spawnSync(process.execPath, ["--test", `--test-reporter=${join(HERE, "lib", "node-test-report.mjs")}`, "diagnostics.test.mjs"], {
    cwd: root,
    encoding: "utf8",
    env,
  });
  const output = result.stdout + result.stderr;
  assert.equal(result.status, 1, output);
  for (const detail of ["a failing native assertion", "native assertion details remain visible", "actual-value", "expected-value", "diagnostics.test.mjs:", "native stdout remains visible", "native stderr remains visible", "native diagnostic remains visible"]) {
    assert.ok(output.includes(detail), `missing failure diagnostic: ${detail}\n${output}`);
  }
  assert.match(output, /at TestContext\./);
  assert.match(output, /tests 4/);
  const reports = readdirSync(reportDir).map((name) => JSON.parse(readFileSync(join(reportDir, name), "utf8")));
  assert.equal(reports.length, 1);
  assert.equal(reports[0].file, "diagnostics.test.mjs");
  assert.equal(reports[0].run_id, "native-diagnostics");
  assert.deepEqual(reports[0].cases, [
    { name: "a passing native test", ok: true },
    { name: "a skipped native test", ok: false },
    { name: "a todo native test", ok: false },
    { name: "a failing native assertion", ok: false },
  ]);
});

test("the native reporter remains readable without structured reporting configured", (t) => {
  const root = mkdtempSync(join(tmpdir(), "talus-node-console-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  writeFileSync(join(root, "console.test.mjs"), `import test from "node:test"; test("a visible native success", () => {});\n`);
  const env = { ...process.env, NODE_DISABLE_COLORS: "1", FORCE_COLOR: "0" };
  delete env.TALUS_UI_REPORT_DIR;
  delete env.NODE_TEST_CONTEXT;
  const result = spawnSync(process.execPath, ["--test", `--test-reporter=${join(HERE, "lib", "node-test-report.mjs")}`, "console.test.mjs"], { cwd: root, encoding: "utf8", env });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /a visible native success/);
  assert.match(result.stdout, /pass 1/);
});
