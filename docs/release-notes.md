# Release notes guide

[简体中文](release-notes.zh-CN.md) · [Back to the release workflow](releasing.md)

New Releases include English and Simplified Chinese descriptions with equivalent
feature, fix, security and upgrade information. CI creates a bilingual draft;
maintainers complete and review it. Translation is not automatic.

## Write and review

- Include user-visible features, fixes, security and compatibility changes.
  Exclude pure tests, CI, coverage and internal refactoring.
- Explain required migration/configuration actions, rollback support and its cost
  in both languages when applicable. Remove both upgrade sections when unnecessary.
- Keep language markers, remove every `<!-- release:todo -->` and preserve the
  `release-image.json` asset unchanged.
- Review generated notes even though [`.github/release.yml`](../.github/release.yml)
  filters labels. Stable/RC releases compare against the highest lower stable tag
  reachable from the release commit; a stable release never compares only with
  its last RC. Without a previous stable tag, GitHub generates the initial history.

## Bilingual template

Write one summary per language. Keep a single shared `What's Changed` and
`Full Changelog` after both blocks; commit subjects do not need translation.

```markdown
<!-- release-notes:en -->
## English

### Highlights
- **Feature.** Describe user-visible behavior.

### Upgrade notes
Required steps, compatibility and rollback consequences. Omit when unnecessary.
<!-- /release-notes:en -->

<!-- release-notes:zh-CN -->
## 简体中文

### 更新亮点
- **功能。** 描述与英文版对应的用户可见行为。

### 升级说明
必要的升级操作、兼容性和回滚代价。无需操作时删除本节。
<!-- /release-notes:zh-CN -->

## What's Changed
* <commit subject> by @<author> in https://github.com/molicherry/Talus/pull/<N>

**Full Changelog**: https://github.com/molicherry/Talus/compare/<previous-stable>...<tag>
```

## Loading bilingual release notes

Use the exact markers from the template on separate lines, at most one block per
language, without nesting. Examples inside code fences are not boundaries, and
Markdown headings do not determine the language.

| Situation | Dialog content |
| --- | --- |
| Simplified Chinese UI | Prefer `zh-CN`, then append shared text |
| English UI | Prefer `en`, then append shared text |
| Preferred language missing or empty | Fall back to the other language, then append shared text |
| Historical notes without markers | Complete original text |
| Unclosed, duplicate, nested or mismatched markers | Complete original text, preserving upgrade/security information |
| Both languages and shared text empty | Localized no-notes message |

The Hub caches the entire GitHub Release body; the frontend selects its language.
Switching languages applies immediately without another GitHub request. Notes are
shown as text: HTML is not executed and translation is not automatic.
**View on GitHub** opens the original Release with both languages. Check/cache
intervals are described in [update notifications](../README.md#update-notifications).

Historical Releases do not need bilingual backfills. When correcting published
notes, update both languages and add a correction note for substantive edits.
Code/artifact changes require a new version under the [release workflow](releasing.md).
