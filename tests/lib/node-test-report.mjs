// Structured evidence for native `node --test` runs.
//
// The gate verifies required implementations by file and outcome. Parsing stdout
// cannot do that: names are global to the output, so a same-named test in another
// directory - or a skipped test with a same-named pass elsewhere - could stand in for
// the required one (S03). This reporter receives node's own events, which carry the
// source file, and writes the same structured report the browser suites write.
//
//   node --test --test-reporter=./tests/lib/node-test-report.mjs <files...>
//
// Report location, run id and suite name come from TALUS_UI_REPORT_DIR,
// TALUS_UI_RUN_ID and TALUS_UI_SUITE (falling back to the run id).
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";

/** Records one test's outcome. A skipped or todo test is not passing evidence. */
export function collect(event, into) {
	const data = event.data ?? {};
	if (!data.name) return;
	const ok = event.type === "test:pass" && !data.skip && !data.todo;
	const file = data.file
		? relative(process.cwd(), data.file).split("\\").join("/")
		: "";
	into.push({ name: data.name, ok, file });
}

export default async function* reporter(source) {
	const cases = [];
	for await (const event of source) {
		if (
			event.type === "test:pass" ||
			event.type === "test:fail" ||
			event.type === "test:skip"
		) {
			collect(event, cases);
		}
	}
	const dir = process.env.TALUS_UI_REPORT_DIR;
	if (!dir) return;
	const suite =
		process.env.TALUS_UI_SUITE ?? process.env.TALUS_UI_RUN_ID ?? "node-test";
	// Group by source file: a case is evidence for the file that really contains it.
	const byFile = new Map();
	for (const entry of cases) {
		if (!byFile.has(entry.file)) byFile.set(entry.file, []);
		byFile.get(entry.file).push({ name: entry.name, ok: entry.ok });
	}
	if (byFile.size === 0) byFile.set("", []);
	for (const [file, entries] of byFile) {
		const suffix = file === "" ? "" : `-${file.replace(/[^a-zA-Z0-9]+/g, "-")}`;
		const path = join(dir, `ui-${suite}${suffix}.json`);
		mkdirSync(dirname(path), { recursive: true });
		writeFileSync(
			path,
			`${JSON.stringify(
				{
					suite,
					run_id: process.env.TALUS_UI_RUN_ID ?? null,
					file,
					cases: entries,
					executed: entries.length,
					failed: entries.filter((entry) => !entry.ok).length,
					filtered: Boolean(process.env.UI_TEST_FILTER),
				},
				null,
				2,
			)}\n`,
		);
	}
}
