// Machine-readable record of which cases a UI/node suite actually executed.
//
// The unified gate verifies required implementations against evidence. Go runners
// report per-test results, but a Node or Playwright suite previously only reported
// its exit code: a filtering environment could leave required cases unexecuted while
// the run still "passed" (F12). Each suite now appends every executed case name and
// writes a JSON report the gate reads.
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const executed = [];
let suite = "ui";

/** Names the suite this report describes (called once, before writing). */
export function nameSuite(next) {
  suite = next;
}

/** Records one executed case (call it for every check/test the suite runs). */
export function recordCase(name) {
  executed.push(name);
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
          cases: executed,
          executed: executed.length,
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
