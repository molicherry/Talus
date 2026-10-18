import { pathToFileURL } from "node:url";
import { GitHub } from "./release.mjs";

export const RELEASE_LABELS = [
  { name: "skip-changelog", description: "Exclude from generated release notes", color: "ededed" },
  { name: "ci", description: "CI and workflow changes only", color: "ededed" },
  { name: "tests", description: "Tests only", color: "ededed" },
  { name: "internal", description: "Internal refactoring only", color: "ededed" },
  { name: "documentation", description: "Improvements or additions to documentation", color: "0075ca" },
];

export async function ensureReleaseLabels(api) {
  const existing = new Set((await api.list("labels")).map(label => label.name.toLowerCase()));
  const created = [];
  for (const label of RELEASE_LABELS) {
    if (existing.has(label.name)) continue;
    await api.request("POST", "labels", label);
    created.push(label.name);
  }
  return { created };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const api = new GitHub(process.env.GITHUB_REPOSITORY, process.env.GH_TOKEN || process.env.GITHUB_TOKEN);
  ensureReleaseLabels(api).then(result => console.log(JSON.stringify(result))).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
