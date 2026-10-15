// Machine-readable record of which cases a UI/node suite actually executed.
//
// The unified gate verifies required implementations against evidence. Go runners
// report per-test results, but a Node or Playwright suite previously only reported
// its exit code: a filtering environment could leave required cases unexecuted while
// the run still "passed" (F12). Each suite now appends every executed case name and
// writes a JSON report the gate reads.
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

// {name, ok}: a case counts as evidence only when it passed, and only for the file
// that registered it (S03).
const executed = [];
let suite = "ui";
// The implementation file this report describes, relative to the runner's cwd: the
// caller names it, because this module is shared and its own path says nothing about
// which test file ran (S03).
let reportPath = "";

/**
 * Names the suite and the implementation file this report describes (called once,
 * before writing). `metaUrl` is the caller's `import.meta.url`.
 */
export function nameSuite(next, metaUrl) {
  suite = next;
  if (metaUrl) {
    reportPath = relative(process.cwd(), fileURLToPath(metaUrl)).split("\\").join("/");
  }
}

/**
 * Records one executed case with its outcome. Call it after the case ran, so a
 * failure is not evidence (S03).
 */
export function recordCase(name, ok = true) {
  executed.push({ name, ok: Boolean(ok) });
}

/** Writes the report; returns the path, or null when reporting is not configured. */
export function writeReport(extra = {}) {
  const dir = process.env.TALUS_UI_REPORT_DIR;
  if (!dir) return null;
  const path = join(dir, `ui-${suite}.json`);
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(
      path,
      `${JSON.stringify(
        {
          suite,
          // The run this evidence belongs to: the gate only accepts reports from the
          // run it just started (R05).
          run_id: process.env.TALUS_UI_RUN_ID ?? null,
          // The file that ran these cases: the implementation identity, not just a
          // name another file could borrow (S03).
          file: reportPath,
          cases: executed,
          executed: executed.length,
          failed: executed.filter((entry) => !entry.ok).length,
          filtered: Boolean(process.env.UI_TEST_FILTER),
          ...extra,
        },
        null,
        2,
      )}\n`,
    );
    return path;
  } catch {
    // Reporting is evidence, not a dependency of running the tests: the gate treats
    // a missing report as a failure, so writing it must not crash the suite.
    return null;
  }
}
