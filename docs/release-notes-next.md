# Upcoming release notes / 下次发布说明

Unreleased text for the next Release draft. / 以下内容尚未发布，供下次 Release 草稿使用。

<!-- release-notes:en -->
## English

### Highlights
- Keep global navigation and system settings save actions visible while scrolling.
- Show CPU, memory, disk and swap usage in independent charts, with shared time-range and statistic controls.
- Clarify agent communication, recent collection, delayed data, buffering and storage errors. Pending setup and initial samples are not shown as faults.
- Add bilingual development and architecture guides and align existing documentation with the implementation.

### Upgrade notes
Source contributors: the root `AGENTS.md` is now tracked. If your checkout has a local untracked or ignored file at that path, back it up **outside the checkout** and move it out of the way before pulling or switching branches. Git may refuse to overwrite an untracked file; ignored files can be overwritten by some checkout operations. Compare your saved instructions with the tracked guide afterward. Container-only deployments are unaffected by this checkout change.
<!-- /release-notes:en -->

<!-- release-notes:zh-CN -->
## 简体中文

### 更新亮点
- 顶部导航和系统设置保存操作区在滚动时保持可见。
- CPU、内存、磁盘和 Swap 使用率拆为独立图表，共用时间范围和统计方式。
- 明确 Agent 通信、最近采集、数据延迟、缓冲和存储错误；等待配置和首个样本不再显示为故障。
- 新增中英文开发与架构指南，并使现有文档与实现一致。

### 升级说明
源码贡献者请注意：根目录 `AGENTS.md` 现已纳入版本控制。如果本地同路径存在未跟踪或被忽略的文件，请在拉取或切换分支前将它备份到**仓库之外**，并移走原文件。Git 可能拒绝覆盖未跟踪文件，而部分检出操作可能直接覆盖被忽略的文件；更新后请对照新的指导文件整理个人规则。仅使用容器部署不受这项源码检出变更影响。
<!-- /release-notes:zh-CN -->
