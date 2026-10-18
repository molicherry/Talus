# Releases and tags

[简体中文](releasing.zh-CN.md)

The release path is: merge to `main` → push an annotated tag → pass all release
gates → publish the version image → review the generated Release draft → publish
the Release → promote the same image digest to `latest`.

## Versions

| Purpose | Tag | GitHub Release | Updates `latest` |
| --- | --- | --- | --- |
| Stable | `v0.12.0` | Required after successful CI; initially a draft | Only the highest published stable |
| Release candidate | `v0.13.0-rc.1` | Draft marked pre-release | Never |
| Internal test | `v0.13.0-test.1` | None | Never |

The `v` prefix is mandatory. Only these three forms are accepted; leading zeros,
build metadata and other suffixes are rejected. RC/test counters start at 1,
increase for each base version and kind, and must never be reused. Maintainers
allocate counters before pushing; CI validates their format, not creation order.

During 0.x development, a patch fixes bugs without changing contracts; a minor
adds functionality and may break compatibility, with explicit upgrade notes.
Reserve major version increments for 1.0 and later.

All new tags, including RC/test tags, must be **annotated** and point to a commit
already on `main`. The message starts with `vX.Y.Z — theme` and includes change
bullets corresponding to the Release Highlights. Historical tags and Releases
are left as they are; missing old Releases are not backfilled.

Once pushed, never delete, move, rename or force-push a tag, even if CI fails.
Retry CI for the same source; use a new version/counter for source changes. Version
image tags are also immutable. On retry, CI verifies and reuses an existing
version image instead of replacing it with a new build.

## Publish a version

1. Merge the changes and synchronize `main`. Decide the version and allocate an
   unused RC/test counter when applicable. Apply `ci`, `tests`, `internal`,
   `documentation`, or `skip-changelog` labels only to PRs consisting entirely of
   those changes. Mixed PRs with user-visible changes must remain in the notes.

   ```bash
   git switch main
   git pull --ff-only origin main
   git fetch origin --tags
   git status --short
   ```

2. Create and check an annotated tag. The example uses a stable version; use the
   appropriate RC/test name throughout when applicable.

   ```bash
   git tag -a v0.12.0 -m "v0.12.0 — <theme>" -m "- <user-visible change>"
   git cat-file -t v0.12.0
   git merge-base --is-ancestor 'v0.12.0^{commit}' origin/main
   git show --no-patch v0.12.0
   git push origin v0.12.0
   ```

3. Open the tag's **CI** run, or find its ID and wait for a successful exit:

   ```bash
   gh run list --workflow ci.yml --branch v0.12.0 --event push --limit 5
   gh run watch <run-id> --exit-status
   ```

   Tag validation, release-policy regressions, backend, frontend, M1–M4, Docker
   acceptance and CodeQL must all pass. Docker exports an OCI archive without
   pushing. After those gates, the publisher copies its exact multi-platform
   digest to `ghcr.io/molicherry/talus:v0.12.0`. It then records the tag object,
   commit, digest and CI run in `release-image.json`. Stable/RC builds create a
   Release draft and attach this evidence. Test builds stop after image publication.
   The CI run listing alone does not prove success.

4. Edit the existing draft in GitHub. Keep the title equal to the tag. Complete
   Highlights, review the generated changes and remove every
   `<!-- release:todo -->` marker. Remove Upgrade notes if unnecessary; otherwise
   explain the migration/configuration changes, required actions, rollback
   support and its cost. Keep the `release-image.json` asset unchanged.

   Release text is English and follows this template:

   ```markdown
   ## Highlights
   - **Feature.** Describe user-visible behavior.

   ## Upgrade notes
   Required steps, compatibility and rollback consequences. Omit when unnecessary.

   ## What's Changed
   * <commit subject> by @<author> in https://github.com/molicherry/Talus/pull/<N>

   **Full Changelog**: https://github.com/molicherry/Talus/compare/<previous-stable>...<tag>
   ```

   Include features, user-visible fixes, security and compatibility changes.
   Exclude pure tests, CI, coverage and internal refactoring. Generated notes use
   the exclusions in [`.github/release.yml`](../.github/release.yml); review them
   manually too. The comparison starts at the greatest lower stable tag reachable
   from this release, including for RCs; a preceding RC is never the stable
   release's baseline. With no previous stable tag, GitHub generates the initial
   history.

5. Publish the reviewed draft. RCs must remain marked pre-release. For a stable
   backport below the highest published version, leave “Set as latest release”
   unchecked.

   ```bash
   gh release edit v0.12.0 --draft=false --latest=false
   ```

6. Watch **Promote published Release**. It revalidates the annotated tag, main
   ancestry, successful tag CI, evidence and both image architectures, then
   copies the highest published stable image **by digest** to `latest`, without
   rebuilding. A global concurrency group and numeric SemVer comparison prevent
   old or out-of-order events from lowering the current version. The job also
   marks that stable Release as GitHub's latest. Verify its success before
   announcing `latest` availability. RC/test tags never advance either latest.

Publishing a Release triggers promotion, not an image build. Images remain
accessible by their version tag while a draft awaits review. Publishing through
the GitHub UI or a maintainer's `gh` token emits the `release.published` event;
do not automate final publication using `GITHUB_TOKEN`, whose events do not start
other workflows.

## Failure and recovery

- Failed gates publish no version image and create no Release. Fix the source in
  a new tag, or rerun the original CI for a transient failure. Never repoint it.
- A failure after image publication may leave the version image without a draft.
  Rerun the failed jobs; the existing image is verified and reused. Draft retries
  preserve reviewed notes and existing evidence, including after publication.
- The OCI archive is retained for 7 days. If it has expired, rerun all CI jobs.
  Existing version tags are still reused; they are never overwritten.
- If a published stable Release has incomplete notes, missing/mismatched evidence,
  failed CI or a modified tag/image, promotion fails and preserves `latest`.
  Investigate the mismatch. Do not remove the check or rewrite evidence to force
  acceptance. Code/artifact mistakes require a new version.
- After correcting a transient API/registry error or Release documentation,
  rerun **Promote published Release**, or invoke recovery:

  ```bash
  gh workflow run release.yml --ref main
  ```

  Recovery always reconciles the highest published stable; it cannot select an
  older version. If multiple published events queue up, the last job still
  reconciles that highest version. An existing newer `latest`, an unlabelled
  image or a same-version digest mismatch is never silently replaced.

Published Releases are not deleted. Correct spelling or erroneous upgrade/security
instructions in place, adding a correction note for substantive documentation
fixes; changed code or artifacts need a new version. The evidence asset is not an
editable release note. Failed tags are retained and are not missing-Release debt.

## Repository setup and rollout

Actions must be allowed to publish to the repository's GHCR package. The workflows
use `GITHUB_TOKEN`: package write only for image publication/promotion, contents
write only for drafts/promotion, and CodeQL's existing scan permissions. No new
personal access token or manual environment approval is required.

Configure tag rulesets restricting creation to release maintainers and blocking
updates/deletion of `v*` tags; CI cannot prevent a privileged owner from bypassing
policy. Merge these workflows before creating the first new-policy tag. Do not
rerun historical tag workflows: those tags retain the old publisher capable of
writing `latest`. Historical Releases without evidence are not eligible for this
automated promotion; recovery is intended for releases created by this workflow.
