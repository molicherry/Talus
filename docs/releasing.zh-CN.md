# Release 与 Tag 规范

[English](releasing.md)

发版顺序：合并到 `main` → 推送 annotated Tag → 全部门禁通过 → 发布版本镜像 →
审核自动生成的 Release 草稿 → 发布 Release → 将同一个镜像 digest 推进到 `latest`。

## 版本与标签

| 用途 | Tag | GitHub Release | 更新 `latest` |
| --- | --- | --- | --- |
| 正式版 | `v0.12.0` | CI 成功后必须发布，先自动生成草稿 | 仅带证据附件的最高已发布正式版 |
| 候选版 | `v0.13.0-rc.1` | 自动生成标为 pre-release 的草稿 | 不更新 |
| 内部测试 | `v0.13.0-test.1` | 不创建 | 不更新 |

必须带 `v`，只接受上述三种形式，不接受前导零、构建元数据或其他后缀。
同一基础版本、同一类型的 RC/test 序号从 1 开始递增，不复用；维护者在推送前分配序号，
CI 校验格式，不推断历史创建顺序。

0.x 阶段，patch 只修 bug，不改契约；minor 增加功能，可引入不兼容变更，但必须在
Upgrade notes 写清影响和操作。major 递增留到 1.0 及以后。

所有新 Tag，包括 RC/test，必须是 **annotated**，指向已经进入 `main` 的提交。
消息首行为 `vX.Y.Z — 一句话主题`，正文包含与 Release Highlights 对应的变更条目。
历史 lightweight Tag、旧 Release 保持原样，不补齐缺失的古早 Release。

Tag 一经推送就不得删除、移动、改名或强推，CI 失败也一样。同一份源码可重跑 CI，
源码需要修改则使用新版本号或新序号。版本镜像标签也不可覆盖；重试时校验并复用已经
发布的镜像，不用新构建替换它。

## 一次完整发版

1. 合并变更并同步 `main`，确定版本号。RC/test 推送前检查序号未占用。
   仅纯 CI、测试、内部重构或文档 PR 使用 `ci`、`tests`、`internal`、
   `documentation`、`skip-changelog` 排除标签；混有用户可见变更的 PR 不能因此被排除。

   ```bash
   git switch main
   git pull --ff-only origin main
   git fetch origin --tags
   git status --short
   ```

2. 创建并检查 annotated Tag。以下以正式版为例，候选版和测试版全程换用相应名称。

   ```bash
   git tag -a v0.12.0 -m "v0.12.0 — <主题>" -m "- <用户可见的变更>"
   git cat-file -t v0.12.0
   git merge-base --is-ancestor 'v0.12.0^{commit}' origin/main
   git show --no-patch v0.12.0
   git push origin v0.12.0
   ```

3. 打开该 Tag 的 **CI**，或找到对应运行 ID 后等待成功退出：

   ```bash
   gh run list --workflow ci.yml --branch v0.12.0 --event push --limit 5
   gh run watch <run-id> --exit-status
   ```

   Tag 校验、发布策略回归、前后端、M1–M4、Docker 验收和 CodeQL 全部通过后，
   才能发布版本镜像。Docker 构建先导出 OCI 归档，不写镜像仓库；发布任务将归档的
   原始多架构 digest 复制为 `ghcr.io/molicherry/talus:v0.12.0`，生成包含 Tag 对象、
   提交、digest、CI 运行 ID 的 `release-image.json`。正式版和 RC 自动生成草稿并附上
   这份证据；测试版只发布镜像。`gh run list` 能查到运行不代表 CI 成功。

4. 在 GitHub 编辑已经生成的草稿。标题默认等于 Tag，也可附加主题，例如
   `v0.12.0 — SSH routes`。流程通过 `tag_name` 识别 Release，重试时保留人工修改的标题。
   补齐 Highlights，审核自动变更列表，
   清除所有 `<!-- release:todo -->` 标记。不涉及升级风险就删除 Upgrade notes；
   涉及迁移、配置或兼容性变化时，写清用户需要做什么、能否回滚，以及回滚代价。
   保留 `release-image.json` 附件原样。

   Release 正文使用英文，模板为：

   ```markdown
   ## Highlights
   - **Feature.** Describe user-visible behavior.

   ## Upgrade notes
   Required steps, compatibility and rollback consequences. Omit when unnecessary.

   ## What's Changed
   * <commit subject> by @<author> in https://github.com/molicherry/Talus/pull/<N>

   **Full Changelog**: https://github.com/molicherry/Talus/compare/<previous-stable>...<tag>
   ```

   保留功能、用户可见的修复、安全和兼容性变化；排除纯测试、CI、覆盖率及内部重构。
   自动变更列表使用 [`.github/release.yml`](../.github/release.yml) 的标签过滤，仍需人工复核。
   正式版和 RC 的比较基线，均为当前提交可达、版本号小于本次版本的最高正式 Tag；
   正式版不会只对比最后一个 RC。不存在前序正式版时使用 GitHub 生成的首次发布历史。

5. 发布审核后的草稿。RC 必须保留 pre-release 标记。补发低于当前最高正式版的旧分支修复时，
   不勾选 “Set as latest release”。也可使用：

   ```bash
   gh release edit v0.12.0 --draft=false --latest=false
   ```

6. 等待 **Promote published Release** 成功。它会再次核验 annotated Tag、main 祖先关系、
   成功的 Tag CI、证据和两种镜像架构，再将带证据附件的最高已发布正式版的**现有 digest** 复制到
   `latest`，不重新构建。全局串行与数值 SemVer 判断共同防止旧版本、乱序事件造成倒退，
   并将相应 Release 标为 GitHub 最新版。成功后再宣布 `latest` 可用。
   RC/test 不推进镜像或 GitHub 的 latest。

发布 Release 只触发镜像晋升，不触发构建。等待草稿审核期间，版本镜像已经可用。
通过 GitHub 页面或维护者自己的 `gh` 身份发布，才能触发后续 `release.published` 工作流；
不要使用 Actions 的 `GITHUB_TOKEN` 自动完成最后发布，它产生的事件不会启动其他工作流。

## 失败与恢复

- 门禁失败，不发布版本镜像、不生成 Release。临时故障重跑原 CI；修改源码则用新 Tag，
  不移动失败的 Tag。
- 镜像发布后、草稿创建前失败，可能留下没有草稿的版本镜像。重跑失败任务会验证并复用镜像，
  不覆盖它。草稿重试保留人工修改的正文及已有证据，已发布 Release 也不会被重写。
- OCI 归档保留 7 天，过期后重跑全部 CI。已经存在的版本镜像仍然只复用、不覆盖。
- 没有 `release-image.json` 附件的历史 Release 会被跳过。若尚无纳入本流程的正式发布，
  恢复会输出 `No stable release is enrolled in this workflow yet; skipping promotion.`
  （尚无纳入本流程的稳定发布，跳过），正常退出，不修改镜像或 Release。
- 已发布正式版若证据内容错误或不匹配、CI 失败、Tag 或镜像被修改、正文仍有占位标记，晋升失败并保留
  原来的 `latest`。调查差异，不删除校验、不修改证据来强行通过；代码或产物出错发新版本。
- 修复临时 API/镜像仓库故障，或纠正 Release 说明后，重跑 **Promote published Release**，
  也可以执行：

  ```bash
  gh workflow run release.yml --ref main
  ```

  恢复始终处理带证据附件的最高已发布正式版，不能指定旧版本降级。多个发布事件排队时，最后执行的任务
  仍会处理已纳入流程的最高版本。现有 `latest` 版本更高、没有版本标签，或同版本 digest 不一致时，
  不会静默覆盖。已有证据附件但内容无效或读取失败时会报错，不会退回较旧版本；
  只有附件不存在才表示该 Release 尚未纳入本流程。

已发布 Release 不删除。笔误和错误的升级/安全说明可原地纠正，实质性文档纠正须附修订说明；
代码和产物变化必须发布新版本。证据附件不是可编辑的版本说明。失败 Tag 保留，也不计入
“成功版本漏发 Release”。

## 仓库配置与首次启用

[`.github/release.yml`](../.github/release.yml) 引用的标签必须先在仓库中存在，维护者才能使用。
在仓库根目录，用具备 issues write 权限且已登录 `gh` 的维护者身份初始化一次：

```bash
GH_TOKEN="$(gh auth token)" GITHUB_REPOSITORY=molicherry/Talus node .github/scripts/release-labels.mjs
```

初始化脚本只创建缺失的 `skip-changelog`、`ci`、`tests`、`internal`、`documentation`，
保留已有标签及其描述和颜色，重复执行不产生变更。这是维护者的一次性配置，
发版任务不因此获得额外的标签管理权限。

仓库 Actions 需要有权写入对应 GHCR 包。工作流使用 `GITHUB_TOKEN`，仅镜像发布/晋升任务
拥有 package write，仅草稿/晋升任务拥有 contents write，并保留 CodeQL 的扫描权限。
不需要新增 PAT 或手动环境审批。

建议使用 Tag ruleset 限制 `v*` 的创建者并禁止修改、删除；CI 无法阻止拥有绕过权限的管理员。
先合并本次工作流改造，再创建第一个新规范版本。不要重跑历史 Tag 的旧工作流，它仍包含
旧的 `latest` 发布逻辑。没有证据附件的历史 Release 不参与新流程的自动晋升；
在首个新流程正式版发布之前执行恢复，会正常跳过。
