// Only explicitly marked, user-facing PR text is copied into a new draft.
// GitHub's generated list owns the release range and changelog exclusions.
const locales = ["en", "zh-CN"];

export function pullRequestNotes(body = "") {
  const notes = {};
  let active;
  let fence;
  for (const line of (body || "").replace(/\r\n?/g, "\n").split("\n")) {
    const delimiter = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
    if (fence || delimiter) {
      if (fence) {
        if (delimiter && delimiter[1][0] === fence.character && delimiter[1].length >= fence.length && !delimiter[2].trim()) fence = undefined;
      } else fence = { character: delimiter[1][0], length: delimiter[1].length };
      if (active) notes[active].push(line);
      continue;
    }
    const marker = /^ {0,3}<!-- (\/?)release-notes:([\w-]+) -->[ \t]*$/.exec(line);
    if (marker) {
      const [, closing, locale] = marker;
      if (!locales.includes(locale) || (closing ? active !== locale : active || Object.hasOwn(notes, locale))) {
        throw new Error("Invalid or duplicate PR release-notes markers");
      }
      if (closing) active = undefined;
      else { active = locale; notes[locale] = []; }
    } else {
      if (/<!--\s*\/?release-notes:/.test(line)) throw new Error("PR release-notes markers must be on separate lines");
      if (active) notes[active].push(line);
    }
  }
  if (active) throw new Error("Unclosed PR release-notes block");
  for (const locale of locales) notes[locale] = (notes[locale] || []).join("\n").trim();
  if (Boolean(notes.en) !== Boolean(notes["zh-CN"])) throw new Error("PR release notes require both English and Simplified Chinese");
  return notes;
}

export async function releaseNotesFromPullRequests(api, generated) {
  if (typeof generated !== "string") throw new Error("Missing generated release notes");
  const escapedRepo = api.repository.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  // Match the source link at the end of a generated PR entry, not references
  // embedded in a title or a Full Changelog comparison link.
  const pattern = new RegExp(`^[-*] .+ in https://github\\.com/${escapedRepo}/pull/([1-9]\\d*)[ \\t]*$`, "gmi");
  const numbers = [...new Set([...generated.replace(/\r\n?/g, "\n").matchAll(pattern)].map(match => match[1]))];
  const entries = [];
  for (const number of numbers) {
    const pull = await api.request("GET", `pulls/${number}`);
    if (String(pull.number) !== number || !pull.merged_at || pull.base?.repo?.full_name?.toLowerCase() !== api.repository.toLowerCase()) {
      throw new Error(`Release notes source is not a merged PR in this repository: #${number}`);
    }
    let notes;
    try { notes = pullRequestNotes(pull.body); }
    catch (error) { throw new Error(`PR #${number}: ${error.message}`); }
    entries.push({ number, notes });
  }
  const instructions = {
    en: "Review and consolidate these PR notes; fill missing summaries and upgrade instructions before publishing.",
    "zh-CN": "发布前请审核并整理以下 PR 说明，补齐缺失的概要和升级要求。",
  };
  const missing = {
    en: "<!-- release:todo --> Add an English summary and any required upgrade instructions for this PR.",
    "zh-CN": "<!-- release:todo --> 补充此 PR 的中文概要及必要的升级要求。",
  };
  return locales.map(locale => {
    const text = entries.length
      ? entries.map(({ number, notes }) => `### [#${number}](https://github.com/${api.repository}/pull/${number})\n\n${notes[locale] || missing[locale]}`).join("\n\n")
      : missing[locale].replace("for this PR", "for this release").replace("此 PR", "本次发布");
    return `<!-- release-notes:${locale} -->\n## ${locale === "en" ? "English" : "简体中文"}\n\n<!-- release:todo --> ${instructions[locale]}\n\n${text}\n<!-- /release-notes:${locale} -->`;
  }).join("\n\n") + `\n\n${generated}`;
}
