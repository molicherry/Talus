import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import {
  assertSuccessfulCI, compareTags, stableReleases, parseTag, previousStable,
  promotionDecision, validateTag,
} from "../.github/scripts/release-policy.mjs";
import { draftRelease, GitHub, promoteLatest, publishImage, Registry } from "../.github/scripts/release.mjs";
import { pullRequestNotes } from "../.github/scripts/release-notes.mjs";
import { ensureReleaseLabels, RELEASE_LABELS } from "../.github/scripts/release-labels.mjs";

const repository = "example/Talus", image = "ghcr.io/example/talus";
const digest = `sha256:${"a".repeat(64)}`, otherDigest = `sha256:${"b".repeat(64)}`;
const commit = "1".repeat(40), tagObject = "2".repeat(40), tag = "v0.12.0";
const receipt = { schema: 1, tag, image, digest, commit, tagObject, runId: "123" };
const info = { tag, commit, object: tagObject };
const successfulRun = {
  status: "completed", conclusion: "success", event: "push", head_sha: commit,
  head_branch: tag, path: ".github/workflows/ci.yml", repository: { full_name: repository },
};
const published = { id: 1, tag_name: tag, name: tag, body: "## Highlights\n- Feature\n\n## What's Changed\n", draft: false, prerelease: false };

function gitFixture(t, callback) {
  const cwd = process.cwd(), root = mkdtempSync(join(tmpdir(), "talus-release-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const git = (...args) => execFileSync("git", args, {
    cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, GIT_AUTHOR_NAME: "Fixture", GIT_AUTHOR_EMAIL: "fixture@example.invalid", GIT_COMMITTER_NAME: "Fixture", GIT_COMMITTER_EMAIL: "fixture@example.invalid" },
  }).trim();
  git("init", "-b", "main");
  git("-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "initial");
  git("update-ref", "refs/remotes/origin/main", "HEAD");
  const annotate = (name, message = `${name} — Feature\n\n- User-visible change`) => git("tag", "-a", name, "-m", message);
  try { process.chdir(root); return callback({ git, annotate }); }
  finally { process.chdir(cwd); }
}

function apiFixture({ releases = [structuredClone(published)], evidence = structuredClone(receipt), evidenceByRelease } = {}) {
  const writes = [], calls = [];
  const api = {
    repository, releases, evidence, evidenceByRelease, run: structuredClone(successfulRun), writes, calls,
    generatedBody: "## What's Changed\n* Feature by @author in https://github.com/example/Talus/pull/1\n\n**Full Changelog**: compare",
    pulls: { 1: { number: 1, merged_at: "2026-10-01T00:00:00Z", base: { repo: { full_name: repository } }, body: "Legacy technical PR description" } },
    async list(path) {
      calls.push(["LIST", path]);
      if (path === "releases") return this.releases;
      const assetMatch = /^releases\/(\d+)\/assets$/.exec(path);
      if (assetMatch) {
        if (this.evidenceByRelease) return Object.hasOwn(this.evidenceByRelease, assetMatch[1]) ? [{ id: Number(assetMatch[1]) + 9000, name: "release-image.json" }] : [];
        return this.evidence ? [{ id: 9, name: "release-image.json" }] : [];
      }
      throw new Error(`Unexpected list: ${path}`);
    },
    async request(method, path, body) {
      calls.push([method, path, body]);
      if (method !== "GET") writes.push([method, path, body]);
      // GitHub's by-tag endpoint only returns published Releases.
      if (method === "GET" && path.startsWith("releases/tags/")) return this.releases.find(item => !item.draft && item.tag_name === path.slice("releases/tags/".length)) || null;
      if (method === "GET" && /^releases\/\d+$/.test(path)) return this.releases.find(item => item.id === Number(path.split("/").at(-1)));
      if (method === "GET" && path === "releases/assets/9") return this.evidence;
      if (method === "GET" && /^releases\/assets\/\d+$/.test(path)) return this.evidenceByRelease[Number(path.split("/").at(-1)) - 9000];
      if (method === "GET" && path === `actions/runs/${receipt.runId}`) return this.run;
      if (method === "GET" && /^pulls\/\d+$/.test(path)) {
        const pull = this.pulls[path.split("/").at(-1)];
        assert.ok(pull, `Unexpected PR lookup: ${path}`);
        return pull;
      }
      if (method === "POST" && path === "releases/generate-notes") return { body: this.generatedBody };
      if (method === "POST" && path === "releases") {
        const result = { ...body, id: 2, html_url: "https://github.com/example/Talus/releases/tag/v0.12.0" };
        this.releases.push(result);
        return result;
      }
      if (method === "POST" && path.includes("/assets?")) { this.evidence = body; return { id: 9 }; }
      if (method === "PATCH" && /^releases\/\d+$/.test(path)) return {};
      throw new Error(`Unexpected request: ${method} ${path}`);
    },
  };
  return api;
}

function registryFixture({ existing = null, current = { tag: "v0.11.0", digest: otherDigest } } = {}) {
  return {
    existing, latest: current, copies: [], inspections: [],
    inspect(ref, expected) {
      this.inspections.push([ref, expected]);
      assert.equal(expected.commit, commit);
      if (ref.startsWith("oci-archive:")) return { digest };
      return this.existing;
    },
    current() { return this.latest; },
    copy(source, destination) {
      this.copies.push([source, destination]);
      if (destination.endsWith(":latest")) this.latest = { tag, digest };
      else this.existing = { digest };
    },
  };
}

test("strict version grammar accepts only stable, rc.N and test.N", () => {
  for (const valid of [tag, "v1.0.0", "v0.12.0-rc.1", "v0.12.0-test.999"]) assert.ok(parseTag(valid));
  for (const invalid of ["0.12.0", "v01.0.0", "v0.1", "v0.1.2-rc.0", "v0.1.2-rc.01", "v0.1.2-beta.1", "v0.1.2+build", "v0.1.2-test.1\n"]) {
    assert.throws(() => parseTag(invalid), /Invalid/);
  }
});

test("SemVer comparison is numeric and does not overflow JavaScript integers", () => {
  for (const [a, b] of [["v0.10.0", "v0.9.0"], [tag, `${tag}-rc.9`], [`${tag}-rc.10`, `${tag}-rc.9`], [`${tag}-rc.1`, `${tag}-test.99`], ["v9007199254740993.0.0", "v9007199254740992.0.0"]]) {
    assert.equal(compareTags(a, b), 1);
    assert.equal(compareTags(b, a), -1);
  }
  assert.equal(compareTags(tag, tag), 0);
});

test("annotated tag on main validates; previous baseline skips RCs and future/unmerged stable tags", t => gitFixture(t, ({ git, annotate }) => {
  git("tag", "v0.9.0"); // Historical lightweight tags remain valid changelog baselines.
  annotate("v0.11.0");
  annotate("v0.12.0-rc.2");
  annotate(tag);
  annotate("v0.13.0");
  assert.equal(validateTag(tag).commit, git("rev-parse", "HEAD"));
  assert.equal(previousStable(tag), "v0.11.0");
  assert.equal(previousStable("v0.12.0-rc.2"), "v0.11.0");
  git("checkout", "-b", "unmerged");
  git("-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "unmerged");
  annotate("v0.11.9");
  assert.equal(previousStable(tag), "v0.11.0");
}));

test("lightweight, off-main, malformed-message and moved tags are rejected", t => gitFixture(t, ({ git, annotate }) => {
  git("tag", tag);
  assert.throws(() => validateTag(tag), /annotated/);
  git("tag", "-d", tag);
  annotate(tag, "incorrect message");
  assert.throws(() => validateTag(tag), /message/);
  git("tag", "-d", tag);
  annotate(tag);
  assert.throws(() => validateTag(tag, commit), /commit differs/);
  assert.throws(() => validateTag(tag, undefined, tagObject), /object differs/);
  git("checkout", "-b", "topic");
  git("-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "topic");
  annotate("v0.13.0");
  assert.throws(() => validateTag("v0.13.0"));
}));

test("latest never regresses or accepts prereleases, missing version labels or same-version digest changes", () => {
  assert.equal(promotionDecision(tag, "v0.11.0", digest, otherDigest), "promote");
  assert.equal(promotionDecision(tag, "v0.13.0", digest, otherDigest), "keep-newer");
  assert.equal(promotionDecision(tag, tag, digest, digest), "unchanged");
  assert.equal(promotionDecision(tag, undefined, digest, undefined), "promote");
  assert.throws(() => promotionDecision(tag, undefined, digest, otherDigest), /no version label/);
  assert.throws(() => promotionDecision(tag, tag, digest, otherDigest), /different digest/);
  for (const kind of ["rc", "test"]) {
    assert.throws(() => promotionDecision(`${tag}-${kind}.1`, "v0.11.0"), /Only stable/);
    assert.throws(() => promotionDecision(tag, `${tag}-${kind}.1`), /not a stable/);
  }
});

test("stable releases are ordered numerically and exclude drafts, prereleases, test tags and invalid names", () => {
  const releases = [published, { ...published, tag_name: "v0.9.0" }, { ...published, tag_name: "v0.13.0", draft: true }, { ...published, tag_name: "v0.14.0", prerelease: true }, { ...published, tag_name: "v0.15.0-test.1" }, { ...published, tag_name: "invalid" }];
  assert.deepEqual(stableReleases(releases).map(release => release.tag_name), [tag, "v0.9.0"]);
});

test("release evidence requires the completed successful CI push for this exact tag, repository and source", () => {
  assert.doesNotThrow(() => assertSuccessfulCI(successfulRun, receipt, repository));
  for (const mutation of [{ status: "in_progress" }, { conclusion: "failure" }, { conclusion: "cancelled" }, { conclusion: "skipped" }, { event: "pull_request" }, { head_sha: "3".repeat(40) }, { head_branch: "main" }, { path: ".github/workflows/other.yml" }, { repository: { full_name: "attacker/Talus" } }]) {
    assert.throws(() => assertSuccessfulCI({ ...successfulRun, ...mutation }, receipt, repository), /successful tag CI/);
  }
});

test("version publication copies the archive once, retaining the built index digest", () => {
  const registry = registryFixture();
  const result = publishImage({ archive: "/tmp/hub.tar", info, image, repository, runId: "123", registry });
  assert.deepEqual(result, receipt);
  assert.deepEqual(registry.copies, [["oci-archive:/tmp/hub.tar", `docker://${image}:${tag}`]]);
});

test("publication retries reuse an existing source-verified version without replacing it", () => {
  const registry = registryFixture({ existing: { digest: otherDigest } });
  const result = publishImage({ archive: "/tmp/new-build.tar", info, image, repository, runId: "123", registry });
  assert.equal(result.digest, otherDigest);
  assert.deepEqual(registry.copies, []);
});

test("publication fails if copying changes the image index digest", () => {
  const registry = registryFixture();
  registry.copy = () => { registry.existing = { digest: otherDigest }; };
  assert.throws(() => publishImage({ archive: "hub.tar", info, image, repository, runId: "123", registry }), /changed.*digest/);
});

test("Registry distinguishes a missing tag from authentication/network failures", () => {
  const registry = new Registry(() => { throw { stderr: "reading manifest: manifest unknown" }; });
  assert.equal(registry.raw("docker://example:missing", true), null);
  for (const stderr of ["unauthorized: authentication required", "connection refused", "too many requests", "HTTP 404 not found"]) {
    const broken = new Registry(() => { throw { stderr }; });
    assert.throws(() => broken.raw("docker://example:missing", true));
  }
});

test("Registry verifies both image architectures and pins label reads to the captured digest", () => {
  const raw = JSON.stringify({ manifests: ["amd64", "arm64"].map(architecture => ({ platform: { os: "linux", architecture } })) });
  const expectedDigest = `sha256:${createHash("sha256").update(raw).digest("hex")}`;
  const calls = [];
  const registry = new Registry((...args) => {
    calls.push(args);
    if (args.includes("--raw")) return raw;
    return JSON.stringify({ config: { Labels: { "org.opencontainers.image.version": tag, "org.opencontainers.image.revision": commit, "org.opencontainers.image.source": `https://github.com/${repository}` } } });
  });
  assert.equal(registry.inspect(`docker://${image}:${tag}`, { tag, commit, repository }).digest, expectedDigest);
  assert.deepEqual(calls.slice(1).map(args => args.at(-1)), Array(2).fill(`docker://${image}@${expectedDigest}`));
  assert.deepEqual(calls.slice(1).map(args => args[args.indexOf("--override-arch") + 1]), ["amd64", "arm64"]);
});

test("Registry rejects a missing architecture or mismatched source labels", () => {
  const incomplete = new Registry(() => JSON.stringify({ manifests: [{ platform: { os: "linux", architecture: "amd64" } }] }));
  assert.throws(() => incomplete.inspect("oci-archive:bad.tar", { tag, commit, repository }), /amd64 and arm64/);
  const wrong = new Registry((...args) => args.includes("--raw")
    ? JSON.stringify({ manifests: ["amd64", "arm64"].map(architecture => ({ platform: { os: "linux", architecture } })) })
    : JSON.stringify({ config: { Labels: {} } }));
  assert.throws(() => wrong.inspect("oci-archive:bad.tar", { tag, commit, repository }), /labels/);
});

test("Registry preserves recognized BuildKit provenance and rejects unrelated extra manifests", () => {
  const manifests = ["amd64", "arm64"].map((architecture, index) => ({ digest: `sha256:${String(index).repeat(64)}`, platform: { os: "linux", architecture } }));
  const attestation = { platform: { os: "unknown", architecture: "unknown" }, annotations: { "vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": manifests[0].digest } };
  const registry = new Registry((...args) => args.includes("--raw")
    ? JSON.stringify({ manifests: [...manifests, attestation] })
    : JSON.stringify({ config: { Labels: { "org.opencontainers.image.version": tag, "org.opencontainers.image.revision": commit, "org.opencontainers.image.source": `https://github.com/${repository}` } } }));
  assert.ok(registry.inspect("oci-archive:hub.tar", { tag, commit, repository }));
  attestation.annotations["vnd.docker.reference.digest"] = otherDigest;
  assert.throws(() => registry.inspect("oci-archive:hub.tar", { tag, commit, repository }), /unrecognized/);
});

for (const kind of ["stable", "rc"]) test(`${kind} creates a draft and evidence with correct flags and an explicit stable baseline`, async () => {
  const api = apiFixture({ releases: [], evidence: null });
  const candidate = { ...receipt, tag: kind === "rc" ? `${tag}-rc.1` : tag };
  await draftRelease({ api, receipt: candidate, baseline: "v0.11.0" });
  const create = api.writes.find(([, path]) => path === "releases")[2];
  assert.equal(create.draft, true);
  assert.equal(create.prerelease, kind === "rc");
  assert.equal(create.make_latest, "false");
  assert.equal(create.name, candidate.tag);
  assert.match(create.body, /release:todo/);
  for (const locale of ["en", "zh-CN"]) {
    const start = `<!-- release-notes:${locale} -->`;
    const end = `<!-- /release-notes:${locale} -->`;
    assert.equal(create.body.split(start).length, 2);
    assert.equal(create.body.split(end).length, 2);
    assert.ok(create.body.indexOf(start) < create.body.indexOf(end));
    assert.match(create.body.slice(create.body.indexOf(start), create.body.indexOf(end)), /release:todo/);
  }
  assert.ok(create.body.indexOf("<!-- /release-notes:zh-CN -->") < create.body.indexOf("## What's Changed"));
  assert.equal(create.body.split("## What's Changed").length, 2, "shared changelog appears once");
  assert.equal(api.writes[0][2].previous_tag_name, "v0.11.0");
  assert.deepEqual(api.evidence, candidate);
});

function noteBlocks(en = "- Visible change.", zh = "- 用户可见变更。") {
  return `<!-- release-notes:en -->\n${en}\n<!-- /release-notes:en -->\n\n<!-- release-notes:zh-CN -->\n${zh}\n<!-- /release-notes:zh-CN -->`;
}

test("new drafts collect marked bilingual PR text and upgrade steps without technical details", async () => {
  const api = apiFixture({ releases: [], evidence: null });
  api.pulls[1].body = `## Implementation\nInternal debugging and validation details.\n\n${noteBlocks(
    "- Keep navigation visible.\n\n**Upgrade notes:** Back up AGENTS.md outside the checkout before pulling.",
    "- 滚动时保持导航可见。\n\n**升级说明：** 拉取前将 AGENTS.md 备份到仓库之外。",
  )}`;
  await draftRelease({ api, receipt, baseline: "v0.11.0" });
  const body = api.releases[0].body;
  const notes = pullRequestNotes(body.slice(0, body.indexOf("## What's Changed")));
  assert.match(notes.en, /navigation visible/);
  assert.match(notes.en, /Back up AGENTS.md outside/);
  assert.match(notes["zh-CN"], /备份到仓库之外/);
  for (const locale of ["en", "zh-CN"]) {
    assert.match(notes[locale], /release:todo/, "human review is required even with complete source notes");
    assert.match(notes[locale], /\[#1\]\(https:\/\/github.com\/example\/Talus\/pull\/1\)/);
  }
  assert.doesNotMatch(body, /Internal debugging/);
  assert.ok(body.endsWith(api.generatedBody));
  assert.deepEqual(api.evidence, receipt);
});

test("the generated changelog owns PR selection; source links are deduplicated and foreign references ignored", async () => {
  const api = apiFixture({ releases: [], evidence: null });
  api.generatedBody = [
    "## What's Changed",
    "* Mentions https://github.com/example/Talus/pull/99 by @author in https://github.com/example/Talus/pull/1",
    "* Another reference by @author in https://github.com/example/Talus/pull/1",
    "* Foreign PR by @author in https://github.com/other/project/pull/2",
    "**Full Changelog**: https://github.com/example/Talus/compare/v0.11.0...v0.12.0",
  ].join("\n");
  api.pulls[1].body = noteBlocks();
  await draftRelease({ api, receipt });
  assert.deepEqual(api.calls.filter(([, path]) => path.startsWith("pulls/")), [["GET", "pulls/1", undefined]]);
  assert.equal(api.releases[0].body.split("### [#1]").length, 3, "one source entry per language");
});

test("legacy and empty PR notes retain per-PR bilingual placeholders", async () => {
  for (const source of [undefined, null, "Legacy PR", noteBlocks("", "")]) {
    const api = apiFixture({ releases: [], evidence: null });
    api.pulls[1].body = source;
    await draftRelease({ api, receipt });
    const notes = pullRequestNotes(api.releases[0].body.slice(0, api.releases[0].body.indexOf("## What's Changed")));
    assert.match(notes.en, /Add an English summary/);
    assert.match(notes["zh-CN"], /补充此 PR/);
    assert.match(notes.en, /\[#1\]/);
    assert.match(notes["zh-CN"], /\[#1\]/);
  }
});

test("single-language PR notes create a draft and defer missing translations to Release review", async () => {
  for (const locale of ["en", "zh-CN"]) {
    const text = locale === "en" ? "- Back up the data before upgrading." : "- 升级前备份数据。";
    const single = `<!-- release-notes:${locale} -->\n${text}\n<!-- /release-notes:${locale} -->`;
    for (const body of [single, noteBlocks(locale === "en" ? text : "", locale === "zh-CN" ? text : "")]) {
      const api = apiFixture({ releases: [], evidence: null });
      api.pulls[1].body = body;
      await draftRelease({ api, receipt });
      const notes = pullRequestNotes(api.releases[0].body.slice(0, api.releases[0].body.indexOf("## What's Changed")));
      assert.ok(notes[locale].includes(text), "retain the provided upgrade instructions");
      const other = locale === "en" ? "zh-CN" : "en";
      assert.match(notes[other], /release:todo/);
      assert.ok(!notes[other].includes(text), "do not substitute untranslated text");
      assert.ok(!notes[locale].includes(locale === "en" ? "Add an English summary" : "补充此 PR"));
      assert.deepEqual(api.evidence, receipt);
    }
  }
});

test("initial releases or direct commits without generated PR entries still require bilingual review", async () => {
  const api = apiFixture({ releases: [], evidence: null });
  api.generatedBody = "## What's Changed\nDirect commit history\n\n**Full Changelog**: compare";
  await draftRelease({ api, receipt });
  assert.match(api.releases[0].body, /for this release/);
  assert.match(api.releases[0].body, /本次发布/);
  assert.ok(!api.calls.some(([, path]) => path.startsWith("pulls/")));
  assert.ok(!Object.hasOwn(api.writes[0][2], "previous_tag_name"));
});

test("PR note parsing supports CRLF and surrounding whitespace without copying unrelated sections", () => {
  const notes = pullRequestNotes(`Internal details\r\n${noteBlocks().replaceAll("\n", "\r\n")}\r\nValidation`);
  assert.deepEqual(notes, { en: "- Visible change.", "zh-CN": "- 用户可见变更。" });
  assert.deepEqual(pullRequestNotes("Legacy description"), { en: "", "zh-CN": "" });
});

test("release-note markers in code examples are content rather than PR language boundaries", () => {
  for (const fence of ["```", "~~~~"]) {
    const example = `${fence}markdown\n${noteBlocks()}\n${fence}`;
    assert.deepEqual(pullRequestNotes(`${example}\n${noteBlocks()}`), { en: "- Visible change.", "zh-CN": "- 用户可见变更。" });
    assert.equal(pullRequestNotes(noteBlocks(`- Summary\n${example}`)).en, `- Summary\n${example}`);
  }
});

test("invalid PR markers abort new draft creation before Release or evidence writes", async () => {
  const invalid = [
    "<!-- /release-notes:en -->",
    "<!-- release-notes:en -->\nUnclosed",
    "<!-- release-notes:en -->\nEnglish\n<!-- /release-notes:zh-CN -->",
    `${noteBlocks()}\n${noteBlocks()}`,
    "<!-- release-notes:fr -->\nUnsupported\n<!-- /release-notes:fr -->",
    "<!-- release-notes:en -->\n<!-- release-notes:zh-CN -->\nNested",
    "Inline <!-- release-notes:en --> not on its own line",
  ];
  for (const body of invalid) {
    const api = apiFixture({ releases: [], evidence: null });
    api.pulls[1].body = body;
    await assert.rejects(draftRelease({ api, receipt }), /PR #1:/);
    assert.equal(api.releases.length, 0);
    assert.equal(api.evidence, null);
    assert.ok(!api.writes.some(([, path]) => path === "releases" || path.includes("/assets?")));
  }
});

test("PR lookup failures and wrong source metadata cannot silently omit release notes", async () => {
  for (const failure of ["api", "unmerged", "repository", "number"]) {
    const api = apiFixture({ releases: [], evidence: null });
    if (failure === "api") {
      const request = api.request.bind(api);
      api.request = async (...args) => {
        if (args[1] === "pulls/1") throw new Error("GitHub HTTP 403");
        return request(...args);
      };
    }
    if (failure === "unmerged") api.pulls[1].merged_at = null;
    if (failure === "repository") api.pulls[1].base.repo.full_name = "other/repo";
    if (failure === "number") api.pulls[1].number = 2;
    await assert.rejects(draftRelease({ api, receipt }), /403|merged PR/);
    assert.equal(api.releases.length, 0);
    assert.equal(api.evidence, null);
  }
});

test("test tags never call the Release API", async () => {
  const api = apiFixture();
  await draftRelease({ api, receipt: { ...receipt, tag: `${tag}-test.1` } });
  assert.deepEqual(api.calls, []);
});

test("draft retries preserve all human edits and published Releases", async () => {
  for (const draft of [true, false]) {
    const title = `${tag} — SSH 路由`;
    const api = apiFixture({ releases: [{ ...published, draft, name: title, body: "Human-reviewed notes" }] });
    await draftRelease({ api, receipt });
    assert.deepEqual(api.writes, []);
    assert.equal(api.releases[0].body, "Human-reviewed notes");
    assert.equal(api.releases[0].name, title);
  }
});

test("draft recovery restores missing evidence to the existing ID and is idempotent", async () => {
  const draft = { ...published, id: 42, draft: true, name: "Reviewed title", body: "Reviewed bilingual notes" };
  const api = apiFixture({ releases: [draft], evidence: null });
  await draftRelease({ api, receipt });
  assert.deepEqual(api.writes, [["POST", "releases/42/assets?name=release-image.json", receipt]]);
  assert.equal(api.releases.length, 1);
  assert.equal(draft.name, "Reviewed title");
  assert.equal(draft.body, "Reviewed bilingual notes");
  api.writes.length = 0;
  await draftRelease({ api, receipt });
  assert.deepEqual(api.writes, []);
});

test("draft lookup finds an existing draft beyond the first API page without using the by-tag endpoint", async () => {
  const calls = [];
  const draft = { ...published, id: 42, draft: true };
  const api = new GitHub(repository, "fixture-token", async (url, options) => {
    const { pathname, searchParams } = new URL(url);
    const path = pathname.slice(`/repos/${repository}/`.length);
    calls.push([options.method, path]);
    if (options.method === "GET" && path === "releases") {
      return new Response(JSON.stringify(searchParams.get("page") === "1"
        ? Array.from({ length: 100 }, (_, i) => ({ ...published, id: 100 + i, tag_name: `v1.0.${i}` }))
        : [draft]));
    }
    if (options.method === "GET" && path === "releases/42") return new Response(JSON.stringify(draft));
    if (options.method === "GET" && path === "releases/42/assets") return new Response("[]");
    if (options.method === "GET" && path.startsWith("releases/tags/")) return new Response("{}", { status: 404 });
    if (options.method === "POST" && path === "releases/42/assets") {
      assert.equal(new URL(url).hostname, "uploads.github.com");
      assert.deepEqual(JSON.parse(options.body), receipt);
      return new Response("{}");
    }
    assert.fail(`Unexpected GitHub request: ${options.method} ${url}`);
  });
  await draftRelease({ api, receipt });
  assert.equal(calls.filter(([, path]) => path === "releases").length, 2);
  assert.ok(!calls.some(([, path]) => path.startsWith("releases/tags/")));
  assert.deepEqual(calls.filter(([method]) => method === "POST"), [["POST", "releases/42/assets"]]);
});

test("duplicate Releases for a tag and a failed draft lookup cannot create or modify a Release", async () => {
  const api = apiFixture({ releases: [published, { ...published, id: 2, draft: true }] });
  await assert.rejects(draftRelease({ api, receipt }), /Multiple Releases/);
  assert.deepEqual(api.writes, []);
  api.list = async () => { throw new Error("GitHub HTTP 403"); };
  await assert.rejects(draftRelease({ api, receipt }), /403/);
  assert.deepEqual(api.writes, []);
});

test("draft retries still reject a different tag or incorrect prerelease flag", async () => {
  for (const mutation of [{ tag_name: "v0.13.0" }, { prerelease: true }]) {
    const api = apiFixture();
    const request = api.request.bind(api);
    api.request = async (...args) => args[1] === "releases/1" ? { ...published, ...mutation } : request(...args);
    await assert.rejects(draftRelease({ api, receipt }), /tag policy/);
    assert.deepEqual(api.writes, []);
  }
});

test("draft retries cannot overwrite mismatched evidence or add assets to a published release", async () => {
  for (const evidence of [{ ...receipt, digest: otherDigest }, null]) {
    const api = apiFixture({ evidence });
    await assert.rejects(draftRelease({ api, receipt }), /different image|already published/);
    assert.deepEqual(api.writes, []);
  }
});

test("stable promotion verifies CI, annotation, source and digest before copying without a build", async () => {
  const api = apiFixture(), registry = registryFixture({ existing: { digest } }), validations = [];
  const result = await promoteLatest({ api, registry, validate: (...args) => validations.push(args) });
  assert.equal(result.decision, "promote");
  assert.deepEqual(validations, [[tag, commit, tagObject]]);
  assert.deepEqual(registry.copies, [[`docker://${image}@${digest}`, `docker://${image}:latest`]]);
  assert.deepEqual(api.writes, [["PATCH", "releases/1", { make_latest: "true" }]]);
});

test("a descriptive Release title does not block promotion or get overwritten", async () => {
  const title = `${tag} — SSH 路由`;
  const api = apiFixture({ releases: [{ ...published, name: title }] });
  const registry = registryFixture({ existing: { digest } });
  assert.equal((await promoteLatest({ api, registry, validate() {} })).decision, "promote");
  assert.equal(api.releases[0].name, title);
  assert.deepEqual(api.writes, [["PATCH", "releases/1", { make_latest: "true" }]]);
});

test("historical releases without evidence make recovery an idempotent successful skip", async () => {
  const api = apiFixture({ releases: [{ ...published, tag_name: "v0.11.0", body: "<!-- release:todo -->" }, { ...published, id: 2, tag_name: "v0.9.0" }], evidence: null });
  const registry = registryFixture();
  for (let attempt = 0; attempt < 2; attempt++) {
    const result = await promoteLatest({ api, registry, validate() { assert.fail("Historical tags must not be validated"); } });
    assert.match(result.skipped, /No stable release is enrolled.*skipping/);
  }
  assert.deepEqual(api.writes, []);
  assert.deepEqual(registry.copies, []);
  assert.deepEqual(registry.inspections, []);
  assert.ok(!api.calls.some(([, path]) => path.startsWith("actions/runs/")));
});

test("recovery finds the highest stable with evidence and skips newer unenrolled releases", async () => {
  const api = apiFixture({ releases: [{ ...published, id: 2, tag_name: "v0.9.0" }, published, { ...published, id: 3, tag_name: "v0.13.0" }], evidenceByRelease: { 1: receipt } });
  const registry = registryFixture({ existing: { digest } });
  assert.equal((await promoteLatest({ api, registry, validate() {} })).tag, tag);
  assert.deepEqual(api.calls.filter(([, path]) => /^releases\/\d+\/assets$/.test(path)), [["LIST", "releases/3/assets"], ["LIST", "releases/1/assets"]]);
  assert.equal(registry.copies.length, 1);
});

test("an invalid evidence asset never falls back to an older enrolled release", async () => {
  for (const evidence of [null, { ...receipt, tag: "v0.13.0", image: "ghcr.io/attacker/talus" }]) {
    const api = apiFixture({ releases: [published, { ...published, id: 3, tag_name: "v0.13.0" }], evidenceByRelease: { 1: receipt, 3: evidence } });
    const registry = registryFixture({ existing: { digest } });
    await assert.rejects(promoteLatest({ api, registry, validate() {} }), /Invalid release image evidence/);
    assert.deepEqual(api.writes, []);
    assert.deepEqual(registry.copies, []);
    assert.ok(!api.calls.some(([, path]) => path === "releases/1/assets"));
  }
});

test("an unreadable evidence asset fails instead of being treated as an unenrolled release", async () => {
  const api = apiFixture({ releases: [published, { ...published, id: 3, tag_name: "v0.13.0" }], evidenceByRelease: { 1: receipt, 3: receipt } });
  const request = api.request.bind(api);
  api.request = async (...args) => {
    if (args[1] === "releases/assets/9003") throw new Error("GitHub HTTP 403");
    return request(...args);
  };
  const registry = registryFixture({ existing: { digest } });
  await assert.rejects(promoteLatest({ api, registry, validate() {} }), /403/);
  assert.deepEqual(api.writes, []);
  assert.deepEqual(registry.copies, []);
  assert.ok(!api.calls.some(([, path]) => path === "releases/1/assets"));
});

test("a delayed/older event reconciles the highest published stable rather than the event tag", async () => {
  const older = { ...published, id: 2, tag_name: "v0.9.0" };
  const api = apiFixture({ releases: [older, published], evidenceByRelease: { 1: receipt, 2: { ...receipt, tag: older.tag_name } } });
  const registry = registryFixture({ existing: { digest } });
  assert.equal((await promoteLatest({ api, registry, publishedRelease: older, validate() {} })).tag, tag);
  assert.equal(registry.copies.length, 1);
});

test("publication events reject temporary tags, missing evidence and mismatched Release state before any writes", async () => {
  for (const mutation of [
    { tag_name: "untagged-9931f697a03729380914" },
    { tag_name: `${tag}-test.1` },
    { draft: true },
    { prerelease: true },
    { id: 99 },
    { tag_name: "v0.13.0" },
  ]) {
    const api = apiFixture(), registry = registryFixture({ existing: { digest } });
    await assert.rejects(promoteLatest({ api, registry, publishedRelease: { ...published, ...mutation }, validate() {} }));
    assert.deepEqual(api.writes, []);
    assert.deepEqual(registry.copies, []);
  }
  const newer = { ...published, id: 3, tag_name: "v0.13.0" };
  for (const evidence of [undefined, null, receipt]) {
    const api = apiFixture({ releases: [published, newer], evidenceByRelease: {
      1: receipt, ...(evidence === undefined ? {} : { 3: evidence }),
    } });
    const registry = registryFixture({ existing: { digest } });
    await assert.rejects(promoteLatest({ api, registry, publishedRelease: newer, validate() {} }),
      evidence === undefined ? /no release-image.json/ : /Invalid release image evidence/);
    assert.deepEqual(api.writes, []);
    assert.deepEqual(registry.copies, []);
    assert.ok(!api.calls.some(([, path]) => path === "releases/1/assets"));
  }
});

test("a valid publication event promotes its verified image", async () => {
  const api = apiFixture(), registry = registryFixture({ existing: { digest } });
  const result = await promoteLatest({ api, registry, publishedRelease: published, validate() {} });
  assert.equal(result.tag, tag);
  assert.equal(result.decision, "promote");
  assert.deepEqual(registry.copies, [[`docker://${image}@${digest}`, `docker://${image}:latest`]]);
});

test("the promotion command reads and rejects invalid publication event payloads before contacting GitHub", t => {
  const root = mkdtempSync(join(tmpdir(), "talus-release-event-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const eventPath = join(root, "event.json");
  for (const [event, error] of [
    [{}, /Publication event is missing its Release/],
    [{ release: { ...published, tag_name: "untagged-9931f697a03729380914" } }, /must use a stable vX.Y.Z tag/],
  ]) {
    writeFileSync(eventPath, JSON.stringify(event));
    assert.throws(() => execFileSync(process.execPath, [fileURLToPath(new URL("../.github/scripts/release.mjs", import.meta.url)), "promote"], {
      env: { ...process.env, GITHUB_REPOSITORY: repository, GITHUB_EVENT_NAME: "release", GITHUB_EVENT_PATH: eventPath },
      stdio: ["ignore", "pipe", "pipe"], timeout: 5_000,
    }), result => result.status === 1 && error.test(String(result.stderr)));
  }
});

test("promotion retries and a newer current latest never rewrite the image", async () => {
  for (const current of [{ tag, digest }, { tag: "v0.13.0", digest: otherDigest }]) {
    const api = apiFixture(), registry = registryFixture({ existing: { digest }, current });
    await promoteLatest({ api, registry, validate() {} });
    assert.deepEqual(registry.copies, []);
    if (current.tag !== tag) assert.deepEqual(api.writes, []);
  }
});

test("promotion fails closed for invalid evidence, a failed CI, tag mismatch or image digest mismatch", async () => {
  for (const failure of ["evidence", "ci", "tag", "digest", "notes"]) {
    const api = apiFixture(), registry = registryFixture({ existing: { digest } });
    if (failure === "evidence") api.evidence = { ...receipt, image: "ghcr.io/attacker/talus" };
    if (failure === "ci") api.run.conclusion = "failure";
    if (failure === "digest") registry.existing = { digest: otherDigest };
    if (failure === "notes") api.releases[0].body = "<!-- release:todo -->";
    await assert.rejects(promoteLatest({ api, registry, validate() { if (failure === "tag") throw new Error("Moved tag"); } }));
    assert.deepEqual(registry.copies, [], failure);
    assert.deepEqual(api.writes, [], failure);
  }
});

test("an RC/test/draft-only release list never updates latest", async () => {
  for (const candidate of [{ ...published, tag_name: `${tag}-rc.1`, prerelease: true }, { ...published, tag_name: `${tag}-test.1` }, { ...published, draft: true }]) {
    const api = apiFixture({ releases: [candidate] }), registry = registryFixture();
    await promoteLatest({ api, registry });
    assert.deepEqual(api.writes, []);
    assert.deepEqual(registry.copies, []);
  }
});

test("GitHub client paginates, uploads JSON evidence and does not hide authentication failures", async () => {
  const calls = [];
  const api = new GitHub(repository, "fixture-token", async (url, options) => {
    calls.push([url, options]);
    if (new URL(url).searchParams.get("page") === "1") return new Response(JSON.stringify(Array(100).fill({ id: 1 })));
    if (new URL(url).searchParams.get("page") === "2") return new Response(JSON.stringify([{ id: 2 }]));
    return new Response("{}", { status: url.includes("forbidden") ? 403 : url.includes("missing") ? 404 : 200 });
  });
  assert.equal((await api.list("releases")).length, 101);
  await api.request("POST", "releases/1/assets?name=release-image.json", receipt, { upload: true });
  assert.ok(calls.at(-1)[0].startsWith("https://uploads.github.com/"));
  assert.deepEqual(JSON.parse(calls.at(-1)[1].body), receipt);
  assert.equal(await api.request("GET", "missing", undefined, { missing: true }), null);
  await assert.rejects(api.request("GET", "forbidden", undefined, { missing: true }), /403/);
});

test("label initialization creates the four missing exclusions and preserves existing metadata on retries", async () => {
  const existing = { name: "documentation", description: "Existing documentation label", color: "123456" };
  const labels = [existing], writes = [];
  const api = {
    async list(path) { assert.equal(path, "labels"); return labels; },
    async request(method, path, body) {
      assert.equal(method, "POST");
      assert.equal(path, "labels");
      writes.push(body);
      labels.push(structuredClone(body));
    },
  };
  assert.deepEqual((await ensureReleaseLabels(api)).created, ["skip-changelog", "ci", "tests", "internal"]);
  assert.equal(writes.length, 4);
  assert.ok(writes.every(label => label.color === "ededed" && label.description.length > 0));
  assert.deepEqual(labels[0], existing);
  assert.deepEqual((await ensureReleaseLabels(api)).created, []);
  assert.equal(writes.length, 4);
});

test("label initialization recognizes existing names case-insensitively and fails on API errors", async () => {
  const existing = RELEASE_LABELS.map(label => ({ ...label, name: label.name.toUpperCase() }));
  const api = { async list() { return existing; }, async request() { assert.fail("Existing labels must be preserved"); } };
  assert.deepEqual((await ensureReleaseLabels(api)).created, []);
  api.list = async () => { throw new Error("GitHub HTTP 401"); };
  await assert.rejects(ensureReleaseLabels(api), /401/);
  api.list = async () => [];
  api.request = async () => { throw new Error("GitHub HTTP 403"); };
  await assert.rejects(ensureReleaseLabels(api), /403/);
});

test("every configured changelog exclusion has label initialization metadata", () => {
  const config = readFileSync(new URL("../.github/release.yml", import.meta.url), "utf8");
  const exclusions = [...config.matchAll(/^\s+- ([\w-]+)$/gm)].map(match => match[1]);
  assert.deepEqual(exclusions.sort(), RELEASE_LABELS.map(label => label.name).sort());
});

test("workflow mutation boundaries require all CI gates, restricted tokens and globally serialized promotion", () => {
  const ci = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const promotion = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  const publishing = ci.slice(ci.indexOf("\n  publish-image:"), ci.indexOf("\n  release-draft:"));
  assert.match(publishing, /needs: \[release-policy, security, backend, frontend, unified, docker, windows-agent\]/);
  assert.equal((ci.match(/packages: write/g) || []).length, 1);
  assert.equal((ci.match(/contents: write/g) || []).length, 1);
  assert.doesNotMatch(ci, /value=latest|push: \$\{\{/);
  assert.match(promotion, /types: \[published\]/);
  assert.match(promotion, /group: stable-release-promotion\n\s+cancel-in-progress: false/);
  assert.match(promotion, /ref: main/);
  assert.doesNotMatch(promotion, /build-push-action|pull_request_target/);
});
