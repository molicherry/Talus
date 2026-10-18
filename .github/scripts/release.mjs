import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import {
  assertReceipt, assertSuccessfulCI, stableReleases, parseTag, previousStable,
  promotionDecision, validateTag,
} from "./release-policy.mjs";

const RECEIPT = "release-image.json";

export class GitHub {
  constructor(repository, token, fetcher = fetch) {
    if (!/^[\w.-]+\/[\w.-]+$/.test(repository)) throw new Error("Invalid repository");
    this.repository = repository;
    this.token = token;
    this.fetcher = fetcher;
  }
  async request(method, path, body, { binary = false, missing = false, upload = false } = {}) {
    const base = upload ? "https://uploads.github.com" : "https://api.github.com";
    const response = await this.fetcher(`${base}/repos/${this.repository}/${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${this.token}`,
        Accept: binary ? "application/octet-stream" : "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(60_000),
    });
    if (missing && response.status === 404) return null;
    if (!response.ok) throw new Error(`GitHub ${method} ${path}: HTTP ${response.status}`);
    if (response.status === 204) return null;
    return binary ? JSON.parse(await response.text()) : response.json();
  }
  async list(path) {
    const result = [];
    for (let page = 1; ; page++) {
      const entries = await this.request("GET", `${path}${path.includes("?") ? "&" : "?"}per_page=100&page=${page}`);
      if (!Array.isArray(entries)) throw new Error(`Expected paginated array: ${path}`);
      result.push(...entries);
      if (entries.length < 100) return result;
    }
  }
}

function command(...args) {
  return execFileSync("skopeo", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], maxBuffer: 4 * 1024 * 1024 });
}

export class Registry {
  constructor(run = command) { this.run = run; }
  raw(ref, missing = false) {
    try { return this.run("inspect", "--raw", ref); }
    catch (error) {
      // Auth/network/rate-limit failures must never be treated as an empty tag.
      if (missing && /\b(manifest unknown|name unknown)\b/i.test(String(error.stderr))) return null;
      throw error;
    }
  }
  inspect(ref, { commit, tag, repository } = {}, missing = false) {
    const raw = this.raw(ref, missing);
    if (raw === null) return null;
    const digest = `sha256:${createHash("sha256").update(raw).digest("hex")}`;
    const pinned = ref.startsWith("docker://") ? `${ref.slice(0, ref.lastIndexOf(":"))}@${digest}` : ref;
    const manifest = JSON.parse(raw);
    const entries = manifest.manifests || [];
    const runnable = entries.filter(item => item.platform?.os !== "unknown");
    const platforms = runnable.map(item => `${item.platform?.os}/${item.platform?.architecture}`).sort();
    if (JSON.stringify(platforms) !== JSON.stringify(["linux/amd64", "linux/arm64"])) {
      throw new Error("Release image must contain exactly Linux amd64 and arm64");
    }
    for (const entry of entries.filter(item => item.platform?.os === "unknown")) {
      if (entry.platform.architecture !== "unknown" || entry.annotations?.["vnd.docker.reference.type"] !== "attestation-manifest" ||
          !runnable.some(item => item.digest === entry.annotations?.["vnd.docker.reference.digest"])) {
        throw new Error("Image index contains an unrecognized extra manifest");
      }
    }
    for (const arch of ["amd64", "arm64"]) {
      const config = JSON.parse(this.run("inspect", "--config", "--override-os", "linux", "--override-arch", arch, pinned));
      const labels = config.config?.Labels || {};
      if (labels["org.opencontainers.image.version"] !== tag || labels["org.opencontainers.image.revision"] !== commit ||
          labels["org.opencontainers.image.source"] !== `https://github.com/${repository}`) {
        throw new Error(`Image ${arch} labels do not match the release tag/source`);
      }
    }
    return { digest };
  }
  current(image) {
    const raw = this.raw(`docker://${image}:latest`, true);
    if (raw === null) return null;
    // Historical images can include provenance manifests. Read their runnable
    // amd64 config, without imposing the new two-manifest layout retroactively.
    const digest = `sha256:${createHash("sha256").update(raw).digest("hex")}`;
    const config = JSON.parse(this.run("inspect", "--config", "--override-os", "linux", "--override-arch", "amd64", `docker://${image}@${digest}`));
    return { tag: config.config?.Labels?.["org.opencontainers.image.version"], digest };
  }
  copy(source, destination) {
    this.run("copy", "--all", "--preserve-digests", source, destination);
  }
}

export function publishImage({ archive, info, image, repository, runId, registry }) {
  const expected = { tag: info.tag, commit: info.commit, repository };
  const candidate = registry.inspect(`oci-archive:${archive}`, expected);
  const evidence = { schema: 1, tag: info.tag, tagObject: info.object, commit: info.commit, image, digest: candidate.digest, runId: String(runId) };
  assertReceipt(evidence, { tag: info.tag, image });
  const destination = `docker://${image}:${info.tag}`;
  let existing = registry.inspect(destination, expected, true);
  if (!existing) {
    registry.copy(`oci-archive:${archive}`, destination);
    existing = registry.inspect(destination, expected);
    if (existing.digest !== candidate.digest) throw new Error("Registry changed the built image digest");
  }
  // A retry may build a different archive (base-image timestamps, for example).
  // Reuse the already published, source-verified version; never rewrite its tag.
  const receipt = { ...evidence, digest: existing.digest };
  assertReceipt(receipt, { tag: info.tag, image });
  return receipt;
}

async function readReceipt(api, release) {
  const assets = await api.list(`releases/${release.id}/assets`);
  const asset = assets.find(item => item.name === RECEIPT);
  return asset ? api.request("GET", `releases/assets/${asset.id}`, undefined, { binary: true }) : undefined;
}

function sameImage(left, right) {
  return ["schema", "tag", "tagObject", "commit", "image", "digest"].every(key => left[key] === right[key]);
}

export async function draftRelease({ api, receipt, baseline }) {
  const kind = parseTag(receipt.tag).kind;
  if (kind === "test") return { skipped: "internal test tag" };
  assertReceipt(receipt, { tag: receipt.tag, image: `ghcr.io/${api.repository.toLowerCase()}` });
  let release = await api.request("GET", `releases/tags/${receipt.tag}`, undefined, { missing: true });
  if (!release) {
    const generated = await api.request("POST", "releases/generate-notes", {
      tag_name: receipt.tag, target_commitish: receipt.commit,
      ...(baseline ? { previous_tag_name: baseline } : {}), configuration_file_path: ".github/release.yml",
    });
    release = await api.request("POST", "releases", {
      tag_name: receipt.tag, target_commitish: receipt.commit, name: receipt.tag,
      draft: true, prerelease: kind === "rc", make_latest: "false",
      body: `## Highlights\n- <!-- release:todo --> Describe the user-visible changes.\n\n## Upgrade notes\n<!-- release:todo --> Document migration, configuration and rollback steps, or remove this section.\n\n${generated.body}`,
    });
  }
  if (release.tag_name !== receipt.tag || release.prerelease !== (kind === "rc")) throw new Error("Existing Release does not match tag policy");
  const recorded = await readReceipt(api, release);
  if (recorded !== undefined) {
    assertReceipt(recorded, { tag: receipt.tag, image: receipt.image });
    if (!sameImage(recorded, receipt)) throw new Error("Release evidence already exists for a different image");
  } else {
    if (!release.draft) throw new Error("Cannot add evidence to an already published Release");
    await api.request("POST", `releases/${release.id}/assets?name=${RECEIPT}`, receipt, { upload: true });
  }
  // Deliberately preserve a human-edited draft and every published Release.
  return { url: release.html_url, draft: release.draft };
}

export async function promoteLatest({ api, registry, validate = validateTag }) {
  // Reconcile the highest enrolled stable, rather than the triggering event.
  // GitHub may replace a pending concurrency-group run with a newer event.
  // Absence of the evidence asset marks historical releases as unenrolled.
  // An existing but invalid/unreadable asset must fail, never fall back.
  let release, receipt;
  for (const candidate of stableReleases(await api.list("releases"))) {
    const evidence = await readReceipt(api, candidate);
    if (evidence === undefined) continue;
    release = candidate;
    receipt = evidence;
    break;
  }
  if (!release) return { skipped: "No stable release is enrolled in this workflow yet; skipping promotion." };
  const tag = release.tag_name, image = `ghcr.io/${api.repository.toLowerCase()}`;
  if (/<!-- release:todo -->/.test(release.body || "")) throw new Error("Review Release notes before promotion");
  assertReceipt(receipt, { tag, image });
  validate(tag, receipt.commit, receipt.tagObject);
  const run = await api.request("GET", `actions/runs/${receipt.runId}`);
  assertSuccessfulCI(run, receipt, api.repository);
  const built = registry.inspect(`docker://${image}:${tag}`, { tag, commit: receipt.commit, repository: api.repository });
  if (built.digest !== receipt.digest) throw new Error("Version image digest no longer matches release evidence");
  const current = registry.current(image);
  const decision = promotionDecision(tag, current?.tag, receipt.digest, current?.digest);
  if (decision === "keep-newer") return { tag, decision };
  if (decision === "promote") {
    registry.copy(`docker://${image}@${receipt.digest}`, `docker://${image}:latest`);
    const updated = registry.current(image);
    if (updated?.digest !== receipt.digest) throw new Error("latest did not retain the version image digest");
  }
  await api.request("PATCH", `releases/${release.id}`, { make_latest: "true" });
  return { tag, digest: receipt.digest, decision };
}

async function main() {
  const [mode, argument] = process.argv.slice(2);
  const repository = process.env.GITHUB_REPOSITORY;
  const tag = process.env.GITHUB_REF_NAME;
  const image = `ghcr.io/${repository?.toLowerCase()}`;
  let result;
  if (mode === "validate") {
    result = validateTag(tag, process.env.GITHUB_SHA);
  } else if (mode === "publish-image") {
    const info = validateTag(tag, process.env.GITHUB_SHA);
    result = publishImage({ archive: argument, info, repository, image, runId: process.env.GITHUB_RUN_ID, registry: new Registry() });
    writeFileSync(RECEIPT, `${JSON.stringify(result, null, 2)}\n`);
  } else if (mode === "draft") {
    const receipt = JSON.parse(readFileSync(argument, "utf8"));
    validateTag(receipt.tag, receipt.commit, receipt.tagObject);
    result = await draftRelease({ api: new GitHub(repository, process.env.GH_TOKEN), receipt, baseline: previousStable(receipt.tag) });
  } else if (mode === "promote") {
    result = await promoteLatest({ api: new GitHub(repository, process.env.GH_TOKEN), registry: new Registry() });
  } else throw new Error("Usage: release.mjs validate | publish-image ARCHIVE | draft RECEIPT | promote");
  console.log(JSON.stringify(result));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
