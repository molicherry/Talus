// Shared release policy. Keep this module free of network/registry writes.
import { execFileSync } from "node:child_process";

export function parseTag(tag) {
  const match = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(rc|test)\.([1-9]\d*))?$/.exec(tag);
  if (!match || match[0] !== tag) throw new Error(`Invalid release tag: ${tag}`);
  return { tag, numbers: match.slice(1, 4).map(BigInt), kind: match[4] || "stable", sequence: BigInt(match[5] || 0) };
}

export function compareTags(left, right) {
  const a = parseTag(left), b = parseTag(right);
  for (let i = 0; i < 3; i++) {
    if (a.numbers[i] !== b.numbers[i]) return a.numbers[i] > b.numbers[i] ? 1 : -1;
  }
  const rank = { test: 0, rc: 1, stable: 2 };
  if (a.kind !== b.kind) return Math.sign(rank[a.kind] - rank[b.kind]);
  return a.sequence === b.sequence ? 0 : a.sequence > b.sequence ? 1 : -1;
}

export function git(...args) {
  return execFileSync("git", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

export function validateTag(tag, expectedCommit, expectedObject) {
  const version = parseTag(tag);
  const ref = `refs/tags/${tag}`;
  if (git("cat-file", "-t", ref) !== "tag") throw new Error("Release tags must be annotated");
  const object = git("rev-parse", ref);
  const commit = git("rev-parse", `${ref}^{commit}`);
  if (expectedCommit && commit !== expectedCommit) throw new Error("Tag commit differs from release evidence");
  if (expectedObject && object !== expectedObject) throw new Error("Tag object differs from release evidence");
  git("merge-base", "--is-ancestor", commit, "refs/remotes/origin/main");
  const message = git("for-each-ref", "--format=%(contents)", ref);
  const [title, ...body] = message.split("\n");
  if (!title.startsWith(`${tag} — `) || !title.slice(tag.length + 3).trim() || !body.some(line => /^- \S/.test(line))) {
    throw new Error("Tag message must have 'vX.Y.Z — theme' and change bullets");
  }
  return { tag, kind: version.kind, commit, object };
}

export function previousStable(tag) {
  return git("tag", "--merged", `refs/tags/${tag}^{commit}`).split("\n").filter(candidate => {
    try { return parseTag(candidate).kind === "stable" && compareTags(candidate, tag) < 0; }
    catch { return false; }
  }).sort((a, b) => compareTags(b, a))[0];
}

export function highestStable(releases) {
  return releases.filter(release => {
    try { return !release.draft && !release.prerelease && parseTag(release.tag_name).kind === "stable"; }
    catch { return false; }
  }).sort((a, b) => compareTags(b.tag_name, a.tag_name))[0];
}

export function assertReceipt(receipt, { tag, image }) {
  if (receipt.schema !== 1 || receipt.tag !== tag || receipt.image !== image ||
      !/^[0-9a-f]{40}$/.test(receipt.commit) || !/^[0-9a-f]{40}$/.test(receipt.tagObject) ||
      !/^sha256:[0-9a-f]{64}$/.test(receipt.digest) || !/^\d+$/.test(String(receipt.runId))) {
    throw new Error("Invalid release image evidence");
  }
}

export function assertSuccessfulCI(run, receipt, repository) {
  if (run.status !== "completed" || run.conclusion !== "success" || run.event !== "push" ||
      run.head_sha !== receipt.commit || run.head_branch !== receipt.tag ||
      run.path !== ".github/workflows/ci.yml" || run.repository?.full_name !== repository) {
    throw new Error("Release image is not backed by a successful tag CI run");
  }
}

export function promotionDecision(candidate, current, expectedDigest, currentDigest) {
  if (parseTag(candidate).kind !== "stable") throw new Error("Only stable releases may update latest");
  if (!current) {
    if (currentDigest) throw new Error("Existing latest has no version label; investigate before recovery");
    return "promote";
  }
  if (parseTag(current).kind !== "stable") throw new Error("Existing latest is not a stable version; investigate before recovery");
  const order = compareTags(candidate, current);
  if (order < 0) return "keep-newer";
  if (order === 0) {
    if (expectedDigest !== currentDigest) throw new Error("Existing latest has the same version but a different digest");
    return "unchanged";
  }
  return "promote";
}
