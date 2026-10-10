#!/usr/bin/env node
// Deterministic coverage report generator and schema validator.
//
//   node tests/render-coverage.mjs          # write tests/coverage.md
//   node tests/render-coverage.mjs --check   # fail if coverage.md is out of date
//
// coverage.json is the single machine-readable source; coverage.md is a
// generated, read-only report. Nothing here may be hand-edited.

import { readFileSync, writeFileSync, existsSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const HERE = dirname(fileURLToPath(import.meta.url));
const JSON_PATH = join(HERE, "coverage.json");
const MD_PATH = join(HERE, "coverage.md");
const ROOT = dirname(HERE);

const SUITES = ["fast", "integration", "ui", "e2e"];
const RUNNERS = ["go-test", "node-test", "playwright", "shell", "manual"];
const STATUSES = ["planned", "implemented", "manual"];
const EXEMPTION_KINDS = ["planning", "history", "checker", "negative_fixture"];
const TOP_KEYS = ["schema_version", "runs", "cases", "gates", "phases", "scan_exemptions"];

function fail(msg) {
  console.error(`coverage.json: ${msg}`);
  process.exit(1);
}

export function loadAndValidate() {
  if (!existsSync(JSON_PATH)) fail("file not found");
  let doc;
  try {
    doc = JSON.parse(readFileSync(JSON_PATH, "utf8"));
  } catch (e) {
    fail(`invalid JSON: ${e.message}`);
  }
  const keys = Object.keys(doc).sort();
  if (JSON.stringify(keys) !== JSON.stringify([...TOP_KEYS].sort())) {
    fail(`top-level keys must be exactly ${TOP_KEYS.join(", ")} (got ${keys.join(", ")})`);
  }
  if (doc.schema_version !== 1) fail("schema_version must be 1");
  if (!Array.isArray(doc.runs) || !Array.isArray(doc.cases)) fail("runs and cases must be arrays");
  if (typeof doc.gates !== "object" || doc.gates === null) fail("gates must be an object");
  if (typeof doc.phases !== "object" || doc.phases === null) fail("phases must be an object");
  if (!Array.isArray(doc.scan_exemptions)) fail("scan_exemptions must be an array");

  const runIds = new Set();
  for (const r of doc.runs) {
    for (const f of ["run_id", "suite", "runner", "cwd", "command", "timeout_seconds", "environment"]) {
      if (!(f in r)) fail(`run missing ${f}`);
    }
    if (runIds.has(r.run_id)) fail(`duplicate run_id ${r.run_id}`);
    runIds.add(r.run_id);
    if (!SUITES.includes(r.suite)) fail(`run ${r.run_id}: suite must be one of ${SUITES.join("/")}`);
    if (!RUNNERS.includes(r.runner)) fail(`run ${r.run_id}: runner must be one of ${RUNNERS.join("/")}`);
    if (typeof r.cwd !== "string" || r.cwd === "") fail(`run ${r.run_id}: cwd must be a non-empty string`);
    if (r.runner === "manual") {
      if (r.command !== null) fail(`run ${r.run_id}: manual runner must use command=null`);
    } else {
      if (!Array.isArray(r.command) || r.command.length === 0 || r.command.some((c) => typeof c !== "string")) {
        fail(`run ${r.run_id}: command must be a non-empty string array`);
      }
      if (!Number.isInteger(r.timeout_seconds) || r.timeout_seconds <= 0) {
        fail(`run ${r.run_id}: timeout_seconds must be a positive integer`);
      }
    }
    if (typeof r.environment !== "object" || r.environment === null) fail(`run ${r.run_id}: environment must be an object`);
    if (typeof r.environment.profile !== "string" || r.environment.profile === "") fail(`run ${r.run_id}: environment.profile required`);
    if (!Array.isArray(r.environment.requires)) fail(`run ${r.run_id}: environment.requires must be an array`);
  }

  for (const [gate, list] of Object.entries(doc.gates)) {
    if (!Array.isArray(list) || list.length === 0) fail(`gate ${gate} must be a non-empty array`);
    for (const id of list) {
      if (!runIds.has(id)) fail(`gate ${gate} references unknown run_id ${id}`);
    }
  }
  for (const [gate, reqs] of Object.entries(doc.phases)) {
    if (!Object.prototype.hasOwnProperty.call(doc.gates, gate)) fail(`phases.${gate} has no matching gate`);
    if (!Array.isArray(reqs) || reqs.length === 0) fail(`phases.${gate} must be a non-empty array`);
    for (const r of reqs) {
      if (!/^REQ-\d{2}$/.test(r)) fail(`phases.${gate}: invalid requirement id ${r}`);
    }
  }
  for (const gate of Object.keys(doc.gates)) {
    if (!Object.prototype.hasOwnProperty.call(doc.phases, gate)) fail(`gate ${gate} has no phases entry`);
  }

  const knownReqs = new Set(Object.values(doc.phases).flat());
  const caseIds = new Set();
  const implIds = new Set();
  for (const c of doc.cases) {
    if (!c.case_id || caseIds.has(c.case_id)) fail(`duplicate or missing case_id ${c.case_id}`);
    caseIds.add(c.case_id);
    if (!/^TC-\d{2}-\d{2}$/.test(c.case_id)) fail(`case_id ${c.case_id} must match TC-XX-YY`);
    if (!Array.isArray(c.requirement_ids) || c.requirement_ids.length === 0) fail(`case ${c.case_id}: requirement_ids required`);
    for (const req of c.requirement_ids) {
      if (!knownReqs.has(req)) fail(`case ${c.case_id}: unknown requirement id ${req}`);
    }
    if (typeof c.spec_ref !== "string" || c.spec_ref === "") fail(`case ${c.case_id}: spec_ref required`);
    if (typeof c.scenario !== "string" || c.scenario.trim() === "") fail(`case ${c.case_id}: scenario required`);
    if (c.scenario.length < 20) fail(`case ${c.case_id}: scenario must be a descriptive English sentence`);
    if (!Array.isArray(c.implementations) || c.implementations.length === 0) fail(`case ${c.case_id}: implementations required`);
    for (const impl of c.implementations) {
      if (!impl.implementation_id || implIds.has(impl.implementation_id)) fail(`duplicate or missing implementation_id ${impl.implementation_id}`);
      implIds.add(impl.implementation_id);
      if (!runIds.has(impl.run_id)) fail(`implementation ${impl.implementation_id}: unknown run_id ${impl.run_id}`);
      if (!STATUSES.includes(impl.automation_status)) fail(`implementation ${impl.implementation_id}: bad automation_status`);
      if (typeof impl.required !== "boolean") fail(`implementation ${impl.implementation_id}: required must be boolean`);
      const run = doc.runs.find((r) => r.run_id === impl.run_id);
      if (impl.automation_status === "implemented") {
        if (!impl.implementation || !impl.implementation.path || !impl.implementation.symbol) {
          fail(`implementation ${impl.implementation_id}: implemented requires implementation.path and symbol`);
        }
        if (run.runner !== "manual" && run.command === null) {
          fail(`implementation ${impl.implementation_id}: implemented requires an executable run`);
        }
        if (!existsSync(join(ROOT, impl.implementation.path))) {
          fail(`implementation ${impl.implementation_id}: path ${impl.implementation.path} does not exist`);
        }
      }
      if (impl.implementation !== null && (typeof impl.implementation.path !== "string" || typeof impl.implementation.symbol !== "string")) {
        fail(`implementation ${impl.implementation_id}: implementation must be null or {path,symbol}`);
      }
    }
  }

  for (const ex of doc.scan_exemptions) {
    if (typeof ex.path !== "string" || ex.path === "") fail("scan_exemption.path required");
    if (!EXEMPTION_KINDS.includes(ex.kind)) fail(`scan_exemption ${ex.path}: kind must be one of ${EXEMPTION_KINDS.join("/")}`);
    if (typeof ex.reason !== "string" || ex.reason.trim().length < 10) fail(`scan_exemption ${ex.path}: English reason required`);
    if (ex.kind !== "negative_fixture" && ex.path.endsWith("/")) fail(`scan_exemption ${ex.path}: only negative_fixture may use a directory prefix`);
    if (ex.path.includes("*")) fail(`scan_exemption ${ex.path}: wildcards are not allowed`);
    // A non-fixture exemption must be a real file inside the repository; a
    // negative fixture must be a real directory. This rejects path="docs".
    const abs = join(ROOT, ex.path);
    if (!existsSync(abs)) fail(`scan_exemption ${ex.path}: path does not exist`);
    const isDir = statSync(abs).isDirectory();
    if (ex.kind === "negative_fixture" && !isDir) fail(`scan_exemption ${ex.path}: negative_fixture must be a directory`);
    if (ex.kind !== "negative_fixture" && isDir) fail(`scan_exemption ${ex.path}: only a negative_fixture may be a directory`);
  }
  return doc;
}

export function render(doc) {
  const lines = [];
  lines.push("# Test coverage");
  lines.push("");
  lines.push("> Generated from `tests/coverage.json` by `tests/render-coverage.mjs`. Do not edit by hand.");
  lines.push("");
  lines.push(`Cases: ${doc.cases.length} · Runs: ${doc.runs.length} · Schema: ${doc.schema_version}`);
  lines.push("");
  lines.push("## Runs");
  lines.push("");
  lines.push("| run_id | suite | runner | cwd | command | timeout |");
  lines.push("| --- | --- | --- | --- | --- | ---: |");
  for (const r of doc.runs) {
    const cmd = r.command === null ? "_(manual)_" : `\`${r.command.join(" ")}\``;
    const to = r.timeout_seconds === null ? "—" : `${r.timeout_seconds}s`;
    lines.push(`| ${r.run_id} | ${r.suite} | ${r.runner} | \`${r.cwd}\` | ${cmd} | ${to} |`);
  }
  lines.push("");
  lines.push("## Gates (pre-registered required runs)");
  lines.push("");
  for (const [gate, list] of Object.entries(doc.gates)) {
    lines.push(`- **${gate}**: ${list.map((x) => `\`${x}\``).join(", ")}`);
  }
  lines.push("");
  lines.push("## Phases (requirement scope per gate)");
  lines.push("");
  for (const [gate, reqs] of Object.entries(doc.phases)) {
    lines.push(`- **${gate}**: ${reqs.join(", ")}`);
  }
  lines.push("");
  lines.push("## Cases");
  lines.push("");
  lines.push("| case_id | requirement | status | run | implementation | required | scenario |");
  lines.push("| --- | --- | --- | --- | --- | --- | --- |");
  for (const c of doc.cases) {
    for (const impl of c.implementations) {
      const loc = impl.implementation ? `\`${impl.implementation.path}:${impl.implementation.symbol}\`` : "—";
      lines.push(
        `| ${c.case_id} | ${c.requirement_ids.join(", ")} | ${impl.automation_status} | ${impl.run_id} | ${loc} | ${impl.required ? "yes" : "no"} | ${c.scenario} |`,
      );
    }
  }
  lines.push("");
  lines.push("## Scan exemptions");
  lines.push("");
  lines.push("| path | kind | reason |");
  lines.push("| --- | --- | --- |");
  for (const ex of doc.scan_exemptions) {
    lines.push(`| \`${ex.path}\` | ${ex.kind} | ${ex.reason} |`);
  }
  lines.push("");
  return lines.join("\n");
}

function main() {
  const check = process.argv.includes("--check");
  const doc = loadAndValidate();
  const rendered = render(doc);

  if (check) {
    if (!existsSync(MD_PATH)) {
      console.error("coverage.md is missing; run: node tests/render-coverage.mjs");
      process.exit(1);
    }
    const current = readFileSync(MD_PATH, "utf8");
    if (current !== rendered) {
      console.error("coverage.md is out of date with coverage.json; run: node tests/render-coverage.mjs");
      process.exit(1);
    }
    console.log(`coverage.json valid; coverage.md up to date (${doc.cases.length} cases, ${doc.runs.length} runs).`);
    return;
  }
  writeFileSync(MD_PATH, rendered);
  console.log(`wrote tests/coverage.md (${doc.cases.length} cases, ${doc.runs.length} runs)`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
