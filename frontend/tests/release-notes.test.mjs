import assert from "node:assert/strict";
import { selectReleaseNotes } from "./.compiled-release-notes.mjs";
import { releaseNotesFromPullRequests } from "../../.github/scripts/release-notes.mjs";

const block = (locale, content) => `<!-- release-notes:${locale} -->\n${content}\n<!-- /release-notes:${locale} -->`;
const english = "## English\n### Highlights\n- Improved SSH routes\n### Upgrade notes\n- Back up first.";
const chinese = "## 简体中文\n### 更新亮点\n- 改进 SSH 路线\n### 升级说明\n- 请先备份。";
const shared = "## What's Changed\n* SSH routes by @author in /pull/1\n\n**Full Changelog**: /compare/old...new";
const bilingual = `${block("en", english)}\n\n${block("zh-CN", chinese)}\n\n${shared}`;

assert.equal(selectReleaseNotes(bilingual, "en"), `${english}\n\n${shared}`);
assert.equal(selectReleaseNotes(bilingual, "zh-CN"), `${chinese}\n\n${shared}`);
assert.equal(selectReleaseNotes(bilingual, "fr"), `${english}\n\n${shared}`);
assert.equal(selectReleaseNotes(bilingual.replaceAll("\n", "\r\n"), "zh-CN"), `${chinese}\n\n${shared}`);
assert.equal(selectReleaseNotes(`${block("zh-CN", chinese)}\n${block("en", english)}`, "en"), english);

for (const missing of ["", block("zh-CN", "  ")]) {
  assert.equal(selectReleaseNotes(`${block("en", english)}\n${missing}\n${shared}`, "zh-CN"), `${english}\n\n${shared}`);
}
assert.equal(selectReleaseNotes(`${block("zh-CN", chinese)}\n${shared}`, "en"), `${chinese}\n\n${shared}`);
for (const locale of ["en", "zh-CN"]) {
  assert.equal(selectReleaseNotes(`  ${english}\n\n${shared}  `, locale), `${english}\n\n${shared}`);
  assert.equal(selectReleaseNotes(" \n ", locale), "");
  assert.equal(selectReleaseNotes(`${block("en", " ")}\n${block("zh-CN", " ")}\n${shared}`, locale), shared);
  assert.equal(selectReleaseNotes(`${block("en", " ")}\n${block("zh-CN", " ")}`, locale), "");
}

for (const fence of ["```", "~~~~"]) {
  const example = `${fence}markdown\n${block("zh-CN", "Example, not a translation")}\n${fence}`;
  assert.equal(selectReleaseNotes(`${block("en", `${english}\n${example}`)}\n${block("zh-CN", chinese)}`, "en"), `${english}\n${example}`);
  assert.equal(selectReleaseNotes(`${example}\n${bilingual}`, "zh-CN"), `${chinese}\n\n${example}\n\n${shared}`);
  assert.equal(selectReleaseNotes(example, "zh-CN"), example);
}

for (const malformed of [
  `${block("en", english)}\n${block("en", "Duplicate")}\n${block("zh-CN", chinese)}`,
  `<!-- release-notes:en -->\n${english}`,
  `<!-- release-notes:en -->\n${block("zh-CN", chinese)}\n<!-- /release-notes:en -->`,
  `${block("en", english)}\n<!-- /release-notes:zh-CN -->`,
  "<!-- release-notes:en -->\nEnglish\n<!-- /release-notes:zh-CN -->",
]) {
  assert.equal(selectReleaseNotes(malformed, "zh-CN"), malformed.trim(), "malformed boundaries must not hide release information");
}

const generated = "## What's Changed\n* SSH routes by @author in https://github.com/example/Talus/pull/1\n\n**Full Changelog**: /compare/old...new";
const collected = await releaseNotesFromPullRequests({
  repository: "example/Talus",
  async request(method, path) {
    assert.equal(method, "GET");
    assert.equal(path, "pulls/1");
    return { number: 1, merged_at: "2026-10-01T00:00:00Z", base: { repo: { full_name: "example/Talus" } }, body: bilingual };
  },
}, generated);
assert.ok(selectReleaseNotes(collected, "en").includes(english));
assert.ok(!selectReleaseNotes(collected, "en").includes(chinese));
assert.ok(selectReleaseNotes(collected, "zh-CN").includes(chinese));
assert.ok(!selectReleaseNotes(collected, "zh-CN").includes(english));
for (const locale of ["en", "zh-CN"]) {
  assert.equal(selectReleaseNotes(collected, locale).split("## What's Changed").length, 2, "collected draft keeps the shared changelog once");
}

const englishOnly = await releaseNotesFromPullRequests({
  repository: "example/Talus",
  async request() {
    return { number: 1, merged_at: "2026-10-01T00:00:00Z", base: { repo: { full_name: "example/Talus" } }, body: block("en", english) };
  },
}, generated);
assert.ok(selectReleaseNotes(englishOnly, "en").includes(english));
assert.match(selectReleaseNotes(englishOnly, "zh-CN"), /补充此 PR/);
assert.ok(!selectReleaseNotes(englishOnly, "zh-CN").includes(english), "missing translation remains visible for Release review");

console.log("Release note language selection, shared changelog, fallback, legacy notes and code fences passed.");
