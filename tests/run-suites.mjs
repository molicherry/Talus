#!/usr/bin/env node
// Suite/gate dispatcher. Reads tests/coverage.json (the single machine source),
// validates it, refuses to run when a required item in the selected scope is not
// implemented (or a required manual item has no evidence), then executes the
// selected runs and writes an execution record.
//
//   node tests/run-suites.mjs --gate M1                      # phase gate (recommended)
//   node tests/run-suites.mjs --suite fast [--phase M1]      # classification; add --phase to scope required items
//   node tests/run-suites.mjs --list

import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadAndValidate } from "./render-coverage.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = dirname(HERE);
const ARTIFACT_DIR = join(HERE, ".artifacts");
const EVIDENCE_DIR = join(HERE, "evidence");
const SUITES = ["fast", "integration", "ui", "e2e"];

function usage(code) {
  console.error(
    "usage: run-suites.mjs --gate <M1..M4> | --suite <fast|integration|ui|e2e|all> [--phase <M1..M4>] | --list",
  );
  process.exit(code);
}

function argValue(flag) {
  const i = process.argv.indexOf(flag);
  return i === -1 ? null : process.argv[i + 1];
}

function selectRuns(doc) {
  if (process.argv.includes("--list")) {
    for (const r of doc.runs) console.log(`${r.run_id}\t${r.suite}\t${r.runner}\t${r.cwd}`);
    for (const [g, ids] of Object.entries(doc.gates)) console.log(`gate:${g}\t${ids.join(",")}`);
    process.exit(0);
  }
  const suite = argValue("--suite");
  const gate = argValue("--gate");
  if (suite && gate) usage(1);
  let ids;
  if (gate) {
    if (!Object.prototype.hasOwnProperty.call(doc.gates, gate)) {
      console.error(`unknown gate ${gate}; known: ${Object.keys(doc.gates).join(", ")}`);
      process.exit(1);
    }
    ids = doc.gates[gate];
  } else if (suite) {
    if (suite !== "all" && !SUITES.includes(suite)) usage(1);
    ids = suite === "all" ? doc.runs.map((r) => r.run_id) : doc.runs.filter((r) => r.suite === suite).map((r) => r.run_id);
  } else {
    usage(1);
  }
  const seen = new Set();
  return doc.runs.filter((r) => ids.includes(r.run_id) && !seen.has(r.run_id) && seen.add(r.run_id));
}

// A gate must cover every in-scope required case with at least one of its runs.
function checkGateCoverage(doc, gate, runs) {
  const scope = new Set(doc.phases[gate]);
  const inGate = new Set(runs.map((r) => r.run_id));
  const missing = [];
  for (const c of doc.cases) {
    if (!c.requirement_ids.some((r) => scope.has(r))) continue;
    for (const impl of c.implementations) {
      if (!impl.required) continue;
      if (!inGate.has(impl.run_id)) missing.push(`${c.case_id}/${impl.implementation_id}`);
    }
  }
  if (missing.length) {
    console.error(`gate ${gate} does not include runs covering required cases: ${missing.join(", ")}`);
    process.exit(1);
  }
}

// scope === null means every requirement is in scope.
function inScope(c, scope) {
  return scope === null || c.requirement_ids.some((r) => scope.has(r));
}

function checkRequiredImplemented(doc, runs, scope, label) {
  const problems = [];
  for (const r of runs) {
    for (const c of doc.cases) {
      if (!inScope(c, scope)) continue;
      for (const impl of c.implementations) {
        if (impl.run_id === r.run_id && impl.required && impl.automation_status === "planned") {
          problems.push(`${c.case_id} (run ${r.run_id})`);
        }
      }
    }
  }
  if (problems.length) {
    console.error(`${label} contains unimplemented required cases: ${problems.join(", ")}`);
    process.exit(1);
  }
}

function manualEvidence(runID) {
  const path = join(EVIDENCE_DIR, `${runID}.json`);
  if (!existsSync(path)) return { ok: false, reason: `missing tests/evidence/${runID}.json` };
  let doc;
  try {
    doc = JSON.parse(readFileSync(path, "utf8"));
  } catch (e) {
    return { ok: false, reason: `invalid evidence JSON: ${e.message}` };
  }
  if (doc.run_id !== runID) return { ok: false, reason: `evidence run_id ${doc.run_id} does not match ${runID}` };
  if (doc.status !== "passed") return { ok: false, reason: `evidence status is ${doc.status}` };
  if (!Array.isArray(doc.evidence) || doc.evidence.length === 0 || doc.evidence.some((e) => typeof e !== "string" || e.trim() === "")) {
    return { ok: false, reason: "evidence must be a non-empty list of non-empty strings" };
  }
  if (typeof doc.commit !== "string" || doc.commit === "") return { ok: false, reason: "evidence.commit is required" };
  const anc = spawnSync("git", ["merge-base", "--is-ancestor", doc.commit, "HEAD"], { cwd: ROOT });
  if (anc.status !== 0) return { ok: false, reason: `evidence commit ${doc.commit} is not in this history` };
  // Reuse is valid only while the code the evidence covers has not changed. The
  // manifest must declare its covered paths; a documentation-only change is
  // always allowed because it does not change shipped behaviour.
  if (!Array.isArray(doc.covers) || doc.covers.length === 0) {
    return { ok: false, reason: "evidence.covers must list the paths the verification covers" };
  }
  for (const c of doc.covers) {
    if (typeof c !== "string" || c.trim() === "" || c.includes("*")) {
      return { ok: false, reason: `invalid evidence.covers entry: ${c}` };
    }
    if (!existsSync(join(ROOT, c))) {
      return { ok: false, reason: `evidence.covers path does not exist: ${c}` };
    }
  }
  // Compare the recorded commit against the actual working tree (staged,
  // unstaged and untracked), so uncommitted edits to covered code also
  // invalidate the evidence. Documentation-only changes are always allowed.
  // -z keeps paths NUL-separated and raw, so non-ASCII or quoted filenames
  // are not mistaken for code changes.
  const diff = spawnSync("git", ["diff", "-z", "--name-only", doc.commit, "--", ...doc.covers], { cwd: ROOT, encoding: "utf8" });
  if (diff.status !== 0) {
    return { ok: false, reason: `cannot diff covered paths against ${doc.commit}` };
  }
  const untracked = spawnSync("git", ["ls-files", "-z", "--others", "--exclude-standard", "--", ...doc.covers], { cwd: ROOT, encoding: "utf8" });
  if (untracked.status !== 0) {
    return { ok: false, reason: "cannot list untracked files under covered paths" };
  }
  const changedCode = [...diff.stdout.split("\0"), ...untracked.stdout.split("\0")]
    .map((f) => f.trim())
    .filter((f) => f !== "" && !f.endsWith(".md"));
  if (changedCode.length) {
    return { ok: false, reason: `covered paths changed since ${doc.commit}: ${[...new Set(changedCode)].slice(0, 5).join(", ")}` };
  }
  return { ok: true };
}

function checkEnvironment(run) {
  for (const req of run.environment.requires) {
    if (req === "playwright") {
      if (!existsSync(join(ROOT, "frontend", "node_modules", "playwright"))) {
        return { ok: false, reason: "requires playwright (run: cd frontend && npm ci)" };
      }
      continue;
    }
    if (!process.env[req]) {
      return { ok: false, reason: `requires env ${req} (export it or choose another suite)` };
    }
  }
  return { ok: true };
}

function main() {
  const doc = loadAndValidate();
  const suite = argValue("--suite");
  const gate = argValue("--gate");
  const phase = argValue("--phase");
  const runs = selectRuns(doc);
  if (runs.length === 0) {
    console.error("no runs selected");
    process.exit(1);
  }

  if (gate) checkGateCoverage(doc, gate, runs);
  let scope = null;
  if (gate) {
    scope = new Set(doc.phases[gate]);
  } else if (phase) {
    if (!Object.prototype.hasOwnProperty.call(doc.phases, phase)) {
      console.error(`unknown phase ${phase}; known: ${Object.keys(doc.phases).join(", ")}`);
      process.exit(1);
    }
    scope = new Set(doc.phases[phase]);
  }
  const label = gate ? `gate ${gate}` : `suite ${suite}${phase ? ` phase ${phase}` : " (all phases)"}`;
  checkRequiredImplemented(doc, runs, scope, label);

  const executionID = `${Date.now().toString(36)}-${process.pid}`;
  const startedAt = new Date().toISOString();
  const commit = spawnSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).stdout.trim();
  const results = [];
  let executed = 0;
  let manual = 0;
  let failed = false;

  console.log(`selected ${runs.length} run(s): ${runs.map((r) => r.run_id).join(", ")}`);
  for (const run of runs) {
    if (run.runner === "manual") {
      const ev = manualEvidence(run.run_id);
      if (!ev.ok) {
        console.error(`✗ manual run ${run.run_id}: pending (${ev.reason})`);
        results.push({ run_id: run.run_id, status: "pending", reason: ev.reason });
        failed = true;
        break;
      }
      manual += 1;
      console.log(`● ${run.run_id}: MANUAL evidence verified`);
      results.push({ run_id: run.run_id, status: "passed", manual: true });
      continue;
    }
    const env = checkEnvironment(run);
    if (!env.ok) {
      console.error(`✗ ${run.run_id}: ${env.reason}`);
      results.push({ run_id: run.run_id, status: "blocked", reason: env.reason });
      failed = true;
      break;
    }
    const cwd = run.cwd === "." ? ROOT : join(ROOT, run.cwd);
    console.log(`▶ ${run.run_id}: ${run.command.join(" ")}  (cwd=${run.cwd})`);
    const res = spawnSync(run.command[0], run.command.slice(1), {
      cwd,
      stdio: "inherit",
      env: process.env,
      timeout: run.timeout_seconds * 1000,
    });
    if (res.error) {
      console.error(`✗ ${run.run_id}: ${res.error.message}`);
      results.push({ run_id: run.run_id, status: "error", reason: res.error.message });
      failed = true;
      break;
    }
    if (res.status !== 0) {
      console.error(`✗ ${run.run_id}: exit ${res.status}`);
      results.push({ run_id: run.run_id, status: "failed", exit: res.status });
      failed = true;
      break;
    }
    executed += 1;
    console.log(`✓ ${run.run_id}`);
    results.push({ run_id: run.run_id, status: "passed" });
  }

  const artifact = {
    execution_id: executionID,
    selector: gate ? { gate } : { suite, phase: phase || null },
    commit,
    started_at: startedAt,
    finished_at: new Date().toISOString(),
    status: failed ? "failed" : "passed",
    environment: { node: process.version, isolated_db: Boolean(process.env.TEST_DATABASE_URL) },
    runs: results,
  };
  mkdirSync(ARTIFACT_DIR, { recursive: true });
  writeFileSync(join(ARTIFACT_DIR, `exec-${executionID}.json`), `${JSON.stringify(artifact, null, 2)}\n`);
  console.log(`record: tests/.artifacts/exec-${executionID}.json`);

  if (failed) process.exit(1);
  console.log(`done (${executed} executed, ${manual} manual).`);
}

main();
