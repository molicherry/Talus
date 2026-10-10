#!/usr/bin/env node
// Suite/gate dispatcher. Reads tests/coverage.json (the single machine source),
// validates it, refuses to run when a required item in the selected set is not
// implemented, then executes the selected runs in file order.
//
//   node tests/run-suites.mjs --suite fast|integration|ui|e2e|all
//   node tests/run-suites.mjs --gate M1
//   node tests/run-suites.mjs --list

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { loadAndValidate } from "./render-coverage.mjs";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const SUITES = ["fast", "integration", "ui", "e2e"];

function usage(code) {
  console.error("usage: run-suites.mjs --suite <fast|integration|ui|e2e|all> | --gate <M1..M4> | --list");
  process.exit(code);
}

function argValue(flag) {
  const i = process.argv.indexOf(flag);
  return i === -1 ? null : process.argv[i + 1];
}

function selectRuns(doc) {
  if (process.argv.includes("--list")) {
    for (const r of doc.runs) console.log(`${r.run_id}\t${r.suite}\t${r.runner}\t${r.cwd}`);
    (Object.entries(doc.gates) || []).forEach(([g, ids]) => console.log(`gate:${g}\t${ids.join(",")}`));
    process.exit(0);
  }
  const suite = argValue("--suite");
  const gate = argValue("--gate");
  if (suite && gate) usage(1);
  let ids;
  if (gate) {
    if (!doc.gates[gate]) {
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

// Correction 1 (#gate): a required implementation in the selected set that is
// still `planned` must block the gate, regardless of whether it is runnable.
function checkRequiredImplemented(doc, runs) {
  const problems = [];
  for (const r of runs) {
    for (const c of doc.cases) {
      for (const impl of c.implementations) {
        if (impl.run_id === r.run_id && impl.required && impl.automation_status === "planned") {
          problems.push(`${c.case_id} (run ${r.run_id}) is required but still planned`);
        }
      }
    }
  }
  if (problems.length) {
    console.error("selected set contains unimplemented required cases:");
    for (const p of problems) console.error(`  - ${p}`);
    process.exit(1);
  }
}

function checkEnvironment(run) {
  for (const req of run.environment.requires) {
    if (req === "playwright") {
      if (!existsSync(join(ROOT, "frontend", "node_modules", "playwright"))) {
        console.error(`run ${run.run_id}: requires playwright (run: cd frontend && npm ci)`);
        process.exit(1);
      }
      continue;
    }
    if (!process.env[req]) {
      console.error(`run ${run.run_id}: requires env ${req} (selected classification needs it; export it or choose a different suite)`);
      process.exit(1);
    }
  }
}

function warnPlanned(doc, runs) {
  const pending = [];
  for (const r of runs) {
    for (const c of doc.cases) {
      for (const impl of c.implementations) {
        if (impl.run_id === r.run_id && impl.required && impl.automation_status === "planned") {
          pending.push(`${c.case_id} (run ${r.run_id})`);
        }
      }
    }
  }
  if (pending.length) {
    console.log(`note: ${pending.length} required implementation(s) in this classification are still planned and not counted as passed: ${pending.join(", ")}`);
  }
}

function main() {
  const doc = loadAndValidate();
  const runs = selectRuns(doc);
  if (runs.length === 0) {
    console.error("no runs selected");
    process.exit(1);
  }
  if (argValue("--gate")) {
    checkRequiredImplemented(doc, runs);
  } else {
    warnPlanned(doc, runs);
  }

  console.log(`selected ${runs.length} run(s): ${runs.map((r) => r.run_id).join(", ")}`);
  let manual = 0;
  for (const run of runs) {
    if (run.runner === "manual") {
      manual += 1;
      console.log(`● ${run.run_id}: MANUAL — evidence tracked outside automation (not counted as passed)`);
      continue;
    }
    checkEnvironment(run);
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
      process.exit(1);
    }
    if (res.status !== 0) {
      console.error(`✗ ${run.run_id}: exit ${res.status}`);
      process.exit(1);
    }
    console.log(`✓ ${run.run_id}`);
  }
  console.log(`done (${runs.length - manual} executed, ${manual} manual).`);
}

main();
