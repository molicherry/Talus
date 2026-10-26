# Release 与 Tag 规范

[English](releasing.md)

维护者从已合并到 `main` 的提交创建 Tag。CI 发布版本镜像并生成 Release 草稿，
人工审核并发布 Release 后，工作流将同一镜像 digest 推进到 `latest`。

[下次发布说明](release-notes-next.md)包含根目录 `AGENTS.md` 纳入版本控制的源码检出注意事项；首次包含该变更的 Release 须带上这段升级说明。

## 必需的 Agent 制品

Windows amd64 制品是 **Hub 启动的硬要求**，即使只监控 Linux 也不能省略。
官方镜像、自定义镜像及离线构建都必须包含同次构建的 Linux amd64、Linux arm64、
Windows amd64 Agent 和匹配的 manifest；缺少 Windows 可执行文件会导致 Hub 启动失败。
使用仓库的 `backend/build/agents.sh` 与 Dockerfile，不要复制旧版仅有 Linux 的制品目录。
启动校验文件大小、SHA-256、平台和协议版本；Windows 必须是 AMD64 控制台 PE 程序。
当前没有可省略平台制品的构建开关。

## 版本与标签

| 类型 | Tag 示例 | CI 成功后的产物 | 更新 `latest` |
| --- | --- | --- | --- |
| 正式版 | `v0.12.0` | 版本镜像、待审核的 Release 草稿；维护者须发布 Release | 仅带证据附件的最高已发布正式版 |
| 候选版 | `v0.13.0-rc.1` | 版本镜像、标为 pre-release 的草稿 | 否 |
| 内部测试 | `v0.13.0-test.1` | 仅版本镜像 | 否 |

- 必须带 `v`，仅接受上述三种形式，不接受前导零、构建元数据或其他后缀。
- 0.x 阶段，patch 只修 bug、不改契约；minor 增加功能，允许不兼容变更，但必须写升级说明。
  major 递增留到 1.0 及以后。
- RC/test 序号按基础版本和类型分别从 1 递增，不复用。维护者分配序号，CI 只校验格式。
- 正式版、RC 和 test 的所有新 Tag 都必须是 annotated Tag，指向已进入 `main` 的提交。
  test 同样受 CI 的主干祖先关系校验约束。消息首行为
  `<完整 Tag> — 一句话主题`，正文列出与发布亮点对应的变更。
- Tag 推送后不得删除、移动、改名或强推，CI 失败也一样。版本镜像标签也不可覆盖。
  同一源码可以重跑 CI；修改源码必须使用新版本号或新序号。

## 准备发布

1. 合并变更，选择版本号，并检查目标 Tag 和 RC/test 序号尚未使用。
2. 同步 `main`，确认工作区干净：

   ```bash
   git switch main
   git pull --ff-only origin main
   git fetch origin --tags
   git status --short
   ```

3. 创建、检查并推送 annotated Tag。以下为正式版示例，RC/test 使用对应名称：

   ```bash
   git tag -a v0.12.0 -m "v0.12.0 — <主题>" -m "- <用户可见的变更>"
   git cat-file -t v0.12.0
   git merge-base --is-ancestor 'v0.12.0^{commit}' origin/main
   git show --no-patch v0.12.0
   git push origin v0.12.0
   ```

## 审核与发布

1. 打开该 Tag 的 **CI** 运行，等待全部检查通过。也可通过 CLI 查看：

   ```bash
   gh run list --workflow ci.yml --branch v0.12.0 --event push --limit 5
   gh run watch <run-id> --exit-status
   ```

   Tag 校验、发布策略回归、前后端、M1–M4、Windows Server 2022 和 2025 上的原生 M5、
   Docker 验收和 CodeQL 全部通过后，CI 才会发布 `ghcr.io/molicherry/talus:v0.12.0`。
   M5 需在管理员会话中运行，命令与前提见 [Windows 监控测试说明](../tests/README.md#windows-monitoring-coverage)。
   镜像包含 Linux amd64/arm64，
   来自 Docker 门禁导出的 OCI 归档。`release-image.json` 记录 Tag 对象、提交、
   镜像 digest 和 CI 运行 ID；正式版和 RC 的草稿自动附上此文件。test 到此完成。

2. 审核自动生成的草稿，按[发布说明规范](#发布说明)补齐中英文内容，
   复核变更列表，清除所有 `<!-- release:todo -->`。涉及升级操作时，两种语言均说明
   迁移、配置、兼容性及回滚代价。保留语言标记和 `release-image.json` 附件。
   标题默认等于 Tag，可附加主题；自动化按 `tag_name` 识别 Release，重试保留人工修改。
   草稿查询遍历 Release 列表的所有分页，再按匹配的 Release ID 读取详情；GitHub 的
   按 Tag 查询接口只返回已发布的 Release。同一 Tag 存在多份 Release 时明确报错，
   由维护者处理重复项，不会任意选取或再建草稿。
3. 使用 GitHub 页面或维护者自己的 `gh` 身份发布审核后的草稿。RC 保留 pre-release 标记，
   补发较低版本的修复时不勾选 “Set as latest release”。CLI 示例：

   ```bash
   gh release edit v0.12.0 --draft=false --latest=false
   ```

4. 正式版等待 **Promote published Release** 成功。工作流复核 Tag、`main` 祖先关系、
   成功的 Tag CI、证据和镜像架构，再将最高已发布且带证据的正式版现有 digest 复制到
   `latest`，并设为 GitHub 最新版。它不重新构建，RC/test 不推进 latest。
   发布事件必须指向已发布、带有匹配 `release-image.json` 证据的正式版 `vX.Y.Z`。
   临时 `untagged-*` 标签或缺失证据会明确报错，不再因处理了旧版本而显示成功；
   合规的延迟事件仍会处理带证据的最高已发布正式版。
5. 确认版本镜像、Release 和晋升结果后，再宣布 `latest` 可用。审核草稿期间，版本镜像已可用。

最后发布 Release 必须产生 `release.published` 事件。不要用 Actions 的 `GITHUB_TOKEN`
自动完成这一步，它产生的事件不会启动后续工作流。

## 发布说明

新 Release 同时提供英文和简体中文，功能、修复、安全及升级信息应一致。
CI 生成双语草稿，维护者补齐并审核内容；不自动翻译。

### 编写与审核

- 更新亮点保留用户可见的功能、修复、安全和兼容性变化，排除纯测试、CI、覆盖率及内部重构。
- 涉及迁移、配置或兼容性变化时，两种语言均说明必要操作、能否回滚及回滚代价；
  无需操作时删除两边的升级说明。
- 保留语言标记，清除所有 `<!-- release:todo -->`。`release-image.json` 附件保持原样。
- 自动变更列表使用 [`.github/release.yml`](../.github/release.yml) 过滤标签，仍须人工复核。
  正式版和 RC 的比较基线均为当前提交可达、且小于本次版本的最高正式 Tag，
  正式版不会只对比最后一个 RC。没有前序正式版时使用 GitHub 生成的首次发布历史。

### 双语模板

两个语言区块各写一份概要，`What's Changed` 和 `Full Changelog` 共享一份，
放在语言区块之后。提交标题无需逐条翻译。

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

### 双语说明的加载规则

标记使用模板中的固定写法，各占一行，每种语言最多一个区块，不允许嵌套。
代码围栏中的示例不会被当作边界，Markdown 标题不用于判断语言。

| 情况 | 弹窗显示 |
| --- | --- |
| 界面为简体中文 | 优先 `zh-CN` 区块，再附上共享正文 |
| 界面为英文 | 优先 `en` 区块，再附上共享正文 |
| 所选语言缺失或为空 | 回退另一语言，再附上共享正文 |
| 历史说明没有语言标记 | 完整原文 |
| 标记未闭合、重复、嵌套或不匹配 | 完整原文，保留升级和安全信息 |
| 两种语言和共享内容均为空 | 当前界面语言的“此版本未提供发布说明” |

Hub 缓存 GitHub Release 完整正文，前端按界面语言选择内容；切换语言立即生效，
无需额外请求 GitHub。正文渲染标题、列表、粗体、代码和链接等 Markdown，
禁用原始 HTML 和图片，仅允许 HTTPS 链接，不自动翻译。
“前往 GitHub”打开含双语全文的原始 Release 页面。
检查及缓存周期见[新版本提示](README.zh-CN.md#新版本提示)。

## 仓库配置

- Actions 需有权写入对应 GHCR 包。工作流使用 `GITHUB_TOKEN`：仅镜像发布/晋升任务拥有
  packages write，仅草稿/晋升任务拥有 contents write，并保留 CodeQL 扫描权限。
  当前流程无需新增 PAT 或手动环境审批。
- [`.github/release.yml`](../.github/release.yml) 中的标签须预先存在。用具备 issues write
  权限且已登录 `gh` 的维护者身份，在仓库根目录初始化一次：

  ```bash
  GH_TOKEN="$(gh auth token)" GITHUB_REPOSITORY=molicherry/Talus node .github/scripts/release-labels.mjs
  ```

  脚本只创建缺失的 `skip-changelog`、`ci`、`tests`、`internal`、`documentation`，
  保留已有标签的描述和颜色。仅纯 CI、测试、内部重构或文档 PR 可使用对应排除标签；
  含用户可见变更的混合 PR 必须保留在变更列表中。
- 建议配置 Tag ruleset，限制 `v*` 的创建者并禁止修改、删除。CI 无法阻止管理员绕过规则。
  首次启用时先合并工作流，再创建新 Tag。历史 Tag/Release 保持原样，不补发古早 Release，
  不重跑仍含旧 latest 发布逻辑的历史 Tag 工作流。

## 失败恢复

| 失败阶段 | 处理方式 |
| --- | --- |
| CI 门禁失败 | 临时故障重跑原 CI；源码修复使用新 Tag。失败 Tag 保留，不发布镜像或 Release。 |
| 镜像已发布，草稿未生成 | 重跑失败任务；已有版本镜像经验证后复用，不覆盖。草稿重试保留人工修改和已有证据。 |
| 现有草稿缺少证据附件 | 使用修正后的草稿查询，在 `release-image-evidence` 产物仍可用时重跑 **Create Release draft**；原始证据会补回同一 Release ID，不重建镜像，也不修改审核后的说明。 |
| OCI 归档已过期 | 归档保留 7 天；过期后重跑全部 CI。已有版本镜像仍只复用。 |
| Release 已发布，晋升失败 | 修复临时 API/镜像仓库故障或说明错误，再重跑晋升任务。代码或产物出错则发新版本。 |
| 发布使用 `untagged-*` 标签或缺少证据 | 发布事件触发的晋升在写入前报错。调查 Release 与 Tag 的关联并恢复原始 Tag/CI 证据；恢复任务处理旧的已纳入流程的版本并显示成功，不代表本次版本已发布。 |
| Tag、证据、CI 或镜像不匹配 | 保留原 latest，调查差异；不得删除校验或修改证据来强行通过。 |
| 尚无带证据的正式发布 | 恢复正常跳过，不修改镜像或 Release。没有证据附件的历史 Release 不参与晋升。 |

Tag CI 重跑使用该 Tag 提交中的工作流和脚本。仅将发布脚本修复合并到 `main`，
不会更新旧 Tag 的运行；旧草稿应使用修正后的脚本和原始 CI 证据恢复，不能移动 Tag
或跳过校验。草稿修复仅在 Release 仍为草稿时补充缺失证据，不会发布草稿或推进 `latest`。

也可从 `main` 手动触发恢复：

```bash
gh workflow run release.yml --ref main
```

恢复始终处理带证据附件的最高已发布正式版。全局串行与数值版本比较防止乱序事件造成降级；
不能指定旧版本覆盖 latest。附件存在但无效或读取失败会报错，不回退到旧版。
现有 latest 版本更高、缺少版本标签，或同版本 digest 不一致时，不会静默覆盖。

已发布 Release 不删除。笔误、升级或安全说明可原地纠正，实质性文档纠正须附修订说明；
代码或产物变化必须发新版本。证据附件不作为可编辑的版本说明。

文档结构参考 [Vite 的发布指南](https://github.com/vitejs/vite/blob/main/CONTRIBUTING.md#release)。
Talus 的自动化实现见 [CI](../.github/workflows/ci.yml)、[晋升工作流](../.github/workflows/release.yml)
和[发布脚本](../.github/scripts/release.mjs)。
