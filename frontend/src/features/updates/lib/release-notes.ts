type ReleaseLocale = "en" | "zh-CN";

/** Select the localized summary and append shared changelog text from a Release body. */
export function selectReleaseNotes(body: string, locale: string): string {
  const original = body.trim();
  const sections: Partial<Record<ReleaseLocale, string[]>> = {};
  const shared: string[][] = [[]];
  let active: ReleaseLocale | null = null;
  let fence: { character: string; length: number } | null = null;

  for (const line of body.replace(/\r\n?/g, "\n").split("\n")) {
    const delimiter = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
    if (fence) {
      if (delimiter && delimiter[1][0] === fence.character && delimiter[1].length >= fence.length && !delimiter[2].trim()) {
        fence = null;
      }
    } else if (delimiter) {
      fence = { character: delimiter[1][0], length: delimiter[1].length };
    } else {
      // Markers inside code examples are content, never language boundaries.
      const marker = /^ {0,3}<!-- (\/?)release-notes:(en|zh-CN) -->[ \t]*$/.exec(line);
      if (marker) {
        const language = marker[2] as ReleaseLocale;
        if (marker[1]) {
          if (active !== language) return original;
          active = null;
          shared.push([]);
        } else {
          // Preserve the full original if blocks are nested, duplicated or mismatched.
          if (active || sections[language]) return original;
          active = language;
          sections[language] = [];
        }
        continue;
      }
    }
    (active ? sections[active]! : shared[shared.length - 1]).push(line);
  }

  if (active || (!sections.en && !sections["zh-CN"])) return original;
  const preferred = locale === "zh-CN" ? "zh-CN" : "en";
  const fallback = preferred === "en" ? "zh-CN" : "en";
  const summary = sections[preferred]?.join("\n").trim() || sections[fallback]?.join("\n").trim();
  const common = shared.map(lines => lines.join("\n").trim()).filter(Boolean).join("\n\n");
  return [summary, common].filter(Boolean).join("\n\n");
}
