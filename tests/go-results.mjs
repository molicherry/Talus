// Interpret native `go test -json` events without promoting a runner exit code
// into proof that its required acceptance tests actually ran.
import { readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";

export function requiredGoTests(doc, run, scope, root) {
  const moduleLine = readFileSync(join(root, run.cwd, "go.mod"), "utf8")
    .match(/^module\s+(\S+)\s*$/m);
  if (!moduleLine) throw new Error(`run ${run.run_id}: go.mod has no module declaration`);
  const expected = [];
  for (const c of doc.cases) {
    if (scope !== null && !c.requirement_ids.some((id) => scope.has(id))) continue;
    for (const impl of c.implementations) {
      if (impl.run_id !== run.run_id || !impl.required || impl.automation_status !== "implemented") continue;
      const path = relative(join(root, run.cwd), join(root, dirname(impl.implementation.path))).replaceAll("\\", "/");
      expected.push({
        case_id: c.case_id,
        implementation_id: impl.implementation_id,
        package: moduleLine[1] + (path === "" ? "" : `/${path}`),
        test: impl.implementation.symbol,
      });
    }
  }
  return expected;
}

export function inspectGoResults(output, expected) {
  const finished = new Map();
  const skipped = [];
  for (const line of output.split("\n")) {
    if (line.trim() === "") continue;
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      throw new Error("go-test runner did not produce valid go test -json output");
    }
    if (!event.Test || !["pass", "fail", "skip"].includes(event.Action)) continue;
    finished.set(`${event.Package}\0${event.Test}`, event.Action);
    if (event.Action === "skip") skipped.push(event);
  }
  return expected.map((item) => {
    let status = finished.get(`${item.package}\0${item.test}`) ?? "not_run";
    // A passing parent does not prove a required scenario when part of it was
    // skipped (for example, because the isolated database was unavailable).
    if (status === "pass" && skipped.some((e) => e.Package === item.package && e.Test.startsWith(`${item.test}/`))) {
      status = "skip";
    }
    return { ...item, status };
  });
}
