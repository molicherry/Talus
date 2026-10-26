# Releases and tags

[简体中文](releasing.zh-CN.md)

Maintainers tag a commit already merged into `main`. CI publishes a versioned
image and creates a Release draft. After a maintainer reviews and publishes it,
the promotion workflow advances the same image digest to `latest`.

## Required agent artifacts

Windows amd64 is a **Hub startup requirement**, even when monitoring only Linux.
Every official, custom or offline Hub build must bundle Linux amd64, Linux arm64
and Windows amd64 agents plus a matching manifest from the same build. Omitting
the Windows executable prevents Hub startup. Use `backend/build/agents.sh` and
the repository Dockerfile; do not copy an older Linux-only artifact directory.
Startup validates each binary's size, SHA-256, platform and protocol; Windows
also requires an AMD64 console PE executable. There is currently no supported
platform-omission switch.

## Versions and tags

| Type | Example tag | Output after successful CI | Updates `latest` |
| --- | --- | --- | --- |
| Stable | `v0.12.0` | Version image and Release draft; maintainer must publish the Release | Only the highest published stable with image evidence |
| Candidate | `v0.13.0-rc.1` | Version image and draft marked pre-release | No |
| Internal test | `v0.13.0-test.1` | Version image only | No |

- The `v` prefix is required. Only these three forms are accepted; leading zeros,
  build metadata and other suffixes are rejected.
- During 0.x development, patches fix bugs without changing contracts. Minors add
  features and may break compatibility with explicit upgrade notes. Reserve major
  increments for 1.0 and later.
- RC/test counters start at 1 for each base version and kind and are never reused.
  Maintainers allocate them; CI validates format, not historical ordering.
- Every new stable, RC or test tag must be annotated and point to a commit
  already on `main`. CI applies the same main-ancestry check to test tags. The
  first message line is `<full tag> — theme`, followed by change bullets matching
  the Release highlights.
- Never delete, move, rename or force-push a published tag, even after failed CI.
  Version image tags are also immutable. Retry CI for the same source; source
  changes require a new version or counter.

## Prepare a release

1. Merge the changes, select a version and confirm the tag/counter is unused.
2. Synchronize `main` and check that the working tree is clean:

   ```bash
   git switch main
   git pull --ff-only origin main
   git fetch origin --tags
   git status --short
   ```

3. Create, inspect and push an annotated tag. Use the corresponding RC/test name
   throughout when applicable:

   ```bash
   git tag -a v0.12.0 -m "v0.12.0 — <theme>" -m "- <user-visible change>"
   git cat-file -t v0.12.0
   git merge-base --is-ancestor 'v0.12.0^{commit}' origin/main
   git show --no-patch v0.12.0
   git push origin v0.12.0
   ```

## Review and publish

1. Open the tag's **CI** run and wait for every check to pass. With the CLI:

   ```bash
   gh run list --workflow ci.yml --branch v0.12.0 --event push --limit 5
   gh run watch <run-id> --exit-status
   ```

   Tag validation, release-policy regressions, backend, frontend, M1–M4, native
   Windows M5 on both Server 2022 and 2025, Docker acceptance and CodeQL must pass
   before publishing `ghcr.io/molicherry/talus:v0.12.0`. M5 runs in an administrator
   session; see [Windows monitoring coverage](../tests/README.md#windows-monitoring-coverage)
   for the command and prerequisites.
   The Linux amd64/arm64 image comes from the OCI archive exported by the Docker
   gate. `release-image.json` records the tag object, commit, digest and CI run ID;
   stable/RC drafts attach it automatically. Test releases are complete here.

2. Review the generated draft using the [release notes guide](#release-notes).
   Complete both languages, review the changelog and remove every
   `<!-- release:todo -->`. Explain migration, configuration, compatibility and
   rollback costs in both languages when applicable. Keep language markers and
   the `release-image.json` asset. The title defaults to the tag and may include
   a theme; automation identifies Releases by `tag_name` and preserves human edits.
   Draft lookup includes every page of the Release list, then reads the matching
   Release by ID; GitHub's by-tag endpoint only returns published Releases.
   Duplicate Releases for the same tag fail for manual resolution instead of
   selecting one or creating another draft.
3. Publish the reviewed draft using the GitHub UI or a maintainer's own `gh`
   identity. Keep RCs marked pre-release. For an older stable backport, leave
   “Set as latest release” unchecked. CLI example:

   ```bash
   gh release edit v0.12.0 --draft=false --latest=false
   ```

4. For stable releases, wait for **Promote published Release**. It rechecks the
   tag, `main` ancestry, successful tag CI, evidence and image architectures, then
   copies the highest published stable with evidence to `latest` by its existing
   digest and marks it latest on GitHub. It does not rebuild. RC/test releases
   never advance either latest.
   A publication event must refer to a published stable `vX.Y.Z` Release with
   matching `release-image.json` evidence. Temporary `untagged-*` tags and
   missing evidence fail explicitly instead of reporting success for an older
   release. A valid delayed event still reconciles the highest enrolled stable.
5. Verify the version image, Release and promotion before announcing `latest`.
   Version images are already available while drafts await review.

Final publication must emit `release.published`. Do not use Actions'
`GITHUB_TOKEN` for that step: its events do not start the subsequent workflow.

## Release notes

New Releases include English and Simplified Chinese descriptions with equivalent
feature, fix, security and upgrade information. PR descriptions hold the inputs;
CI collects them into a bilingual GitHub Release draft, where maintainers
consolidate, complete and review the notes. Translation is not automatic. Published
Releases retain the version history; no separate upcoming-notes file is maintained.

Automation prepares a draft, not publication. Maintainers may replace collected
text with hand-written English and Chinese release notes; generated wording is
not mandatory. Review and final publication remain manual.

[PR #65](https://github.com/molicherry/Talus/pull/65) carries the source-checkout
upgrade note for the newly tracked root `AGENTS.md`. The first Release containing
that change must include its backup/move instructions. If that Release already
has a draft, add the note there manually; draft recovery preserves reviewed text.

### Write and review

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

### Pull request notes

PR descriptions may use English or Simplified Chinese; one language is sufficient.
The release-note blocks in the [PR template](../.github/pull_request_template.md)
are optional. Fill either language with short user-facing Markdown and any required
migration/configuration actions and rollback limits. Only the marked text is copied,
not the PR's technical summary or validation details. Maintainers complete missing
translations in the Release draft. The following shows the supported languages;
only one block is needed in a PR:

```markdown
<!-- release-notes:en -->
- Keep navigation visible while scrolling.

**Upgrade notes:** Describe required actions and rollback limits, if any.
<!-- /release-notes:en -->

<!-- release-notes:zh-CN -->
- 滚动时保持导航可见。

**升级说明：** 如有必要，说明操作步骤和回滚限制。
<!-- /release-notes:zh-CN -->
```

- Put each marker on its own line, at most one block per language. A single-language
  note is accepted; the missing language receives a translation placeholder in the
  Release draft. Malformed or duplicate markers still stop draft creation so they
  cannot silently lose upgrade instructions.
- For purely internal changes, leave both blocks empty or remove both, and apply
  the appropriate exclusion label. A change with user-facing upgrade requirements
  must not be excluded as documentation/internal work.
- The collector reads the repository's merged PR source links from GitHub's
  generated `What's Changed` entries. This keeps the same release range and
  [label exclusions](../.github/release.yml) as that list, including RC baselines.
  API failures stop draft creation; they are not treated as empty PR notes.
- An included PR without marked notes gets a per-PR `release:todo` in each
  language; a single-language PR retains its text and gets a placeholder only for
  the missing language. Releases with no PR entries get general placeholders,
  including changes committed directly. Complete them using the tag and commit history.
- Every new draft retains a review `release:todo` even when all PRs supplied both
  languages. Consolidate repetitive entries and check upgrade requirements before
  removing the markers and publishing. PR descriptions remain the source inputs;
  the Release draft owns the reviewed text.
- Retries never refresh or overwrite an existing draft or published Release. If
  source notes change after a draft is created, update that draft explicitly.

### Bilingual template

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

### Loading bilingual release notes

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
rendered as Markdown headings, lists, emphasis, code and links. Raw HTML and images
are disabled, links require HTTPS, and translation is not automatic.
**View on GitHub** opens the original Release with both languages. Check/cache
intervals are described in [update notifications](../README.md#update-notifications).

## Repository configuration

- Actions must be allowed to write the repository's GHCR package. Workflows use
  `GITHUB_TOKEN`: packages write only for image publication/promotion, contents
  write only for drafts/promotion, plus CodeQL's scanning permissions. This flow
  requires no new PAT or manual environment approval.
- Labels referenced by [`.github/release.yml`](../.github/release.yml) must exist.
  Initialize them once from the repository root using an authenticated maintainer
  `gh` identity with issues write permission:

  ```bash
  GH_TOKEN="$(gh auth token)" GITHUB_REPOSITORY=molicherry/Talus node .github/scripts/release-labels.mjs
  ```

  The script creates missing `skip-changelog`, `ci`, `tests`, `internal` and
  `documentation` labels, preserving existing descriptions/colors. Apply exclusions
  only to PRs consisting entirely of those changes; mixed PRs with user-visible
  behavior must remain in the changelog.
- Configure a tag ruleset restricting creation of `v*` tags and blocking updates
  and deletion. CI cannot prevent an administrator bypass. For initial rollout,
  merge workflows before creating tags. Preserve historical tags/Releases, do not
  backfill old Releases, and do not rerun historical tag workflows that still
  contain the old latest publisher.

## Recovery

| Failure stage | Action |
| --- | --- |
| CI gate fails | Retry transient failures; source fixes need a new tag. Keep the failed tag; publish no image or Release. |
| Image exists but draft creation fails | Rerun failed jobs; verify and reuse the existing image. Draft retries preserve human edits and evidence. |
| Existing draft is missing its evidence asset | With the corrected draft lookup, rerun **Create Release draft** while its `release-image-evidence` artifact is available. It restores the original evidence to the same Release ID without rebuilding or changing reviewed notes. |
| OCI archive expires | Archives last 7 days; rerun all CI after expiry. Existing version images remain immutable. |
| Published Release fails promotion | Correct transient API/registry failures or notes, then retry promotion. Code/artifact mistakes need a new version. |
| Publication uses an `untagged-*` tag or has no evidence | Publication-triggered promotion fails before writes. Investigate the Release association and recover the original tag/CI evidence; a successful recovery run for an older enrolled release does not establish that this version was released. |
| Tag, evidence, CI or image mismatch | Preserve latest and investigate. Do not remove checks or rewrite evidence to force acceptance. |
| No stable release has evidence yet | Recovery skips without changing images or Releases. Historical Releases without evidence are excluded. |

Tag CI reruns use the workflow and script from that tag's commit. Merging a
release-script fix into `main` does not update older tag runs; recover their
drafts using the corrected script and original CI evidence, without moving the tag
or bypassing validation. Draft repair only attaches missing evidence while the
Release is still a draft; it does not publish the draft or advance `latest`.

To trigger recovery manually from `main`:

```bash
gh workflow run release.yml --ref main
```

Recovery always selects the highest published stable with evidence. Global
serialization and numeric version comparison prevent out-of-order events from
downgrading latest; callers cannot select an older version. An existing invalid
or unreadable evidence asset fails instead of falling back to an older release.
A newer latest, missing version label or same-version digest mismatch is never
silently overwritten.

Do not delete published Releases. Correct spelling or upgrade/security instructions
in place, with a correction note for substantive edits. Code/artifact changes need
a new version. The evidence asset is not an editable release note.

Structure follows [Vite's release guide](https://github.com/vitejs/vite/blob/main/CONTRIBUTING.md#release).
Talus automation is defined in [CI](../.github/workflows/ci.yml),
[the promotion workflow](../.github/workflows/release.yml) and
[the release script](../.github/scripts/release.mjs).
