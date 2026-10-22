# 发布说明规范

[English](release-notes.md) · [返回发布流程](releasing.zh-CN.md)

新 Release 同时提供英文和简体中文，功能、修复、安全及升级信息应一致。
CI 生成双语草稿，维护者补齐并审核内容；不自动翻译。

## 编写与审核

- 更新亮点保留用户可见的功能、修复、安全和兼容性变化，排除纯测试、CI、覆盖率及内部重构。
- 涉及迁移、配置或兼容性变化时，两种语言均说明必要操作、能否回滚及回滚代价；
  无需操作时删除两边的升级说明。
- 保留语言标记，清除所有 `<!-- release:todo -->`。`release-image.json` 附件保持原样。
- 自动变更列表使用 [`.github/release.yml`](../.github/release.yml) 过滤标签，仍须人工复核。
  正式版和 RC 的比较基线均为当前提交可达、且小于本次版本的最高正式 Tag，
  正式版不会只对比最后一个 RC。没有前序正式版时使用 GitHub 生成的首次发布历史。

## 双语模板

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

## 双语说明的加载规则

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
无需额外请求 GitHub。正文作为文本展示，不执行 HTML，不自动翻译。
“前往 GitHub”打开含双语全文的原始 Release 页面。
检查及缓存周期见[新版本提示](README.zh-CN.md#新版本提示)。

历史 Release 不要求补写双语。纠正已发布说明时，两种语言同步更新；
实质性纠正附修订说明，代码或产物变化按[发布流程](releasing.zh-CN.md)使用新版本。
