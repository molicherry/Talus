# Phase 1 / Phase 2 修复与验证

修复日期：2026-10-11 至 2026-10-12。基线：`feat/talus-hardening` 的 `718d7692766550663c950020986a25f9a19dcadd`。

修复在独立工作区 `/workspace/Talus-hardening` 完成，交付分支为 `feat/talus-hardening`。本记录对应此前审查中的 4 个 P1、8 个 P2；不扩展 Phase 3 / Phase 4 功能范围。

## 修复对应关系

| 编号 | 修复 | 主要回归证据 |
| --- | --- | --- |
| R1 | 首次安装先原子创建用户基础表和版本列，再完成普通模型迁移；旧库升级不重置版本 | 空库、旧库及并发迁移；真实 server 进程启动、bootstrap、改密、重启 |
| R2 | JWT 版本核验与 preparing 登记共享用户锁；ready、输入和 resize 也经过版本核验 | 真实 HTTP / WebSocket Handler 与受控 SSH 对端重放登记、ready、输入和改密竞态 |
| R3 | 区分确定回滚与 COMMIT 结果未知；未知时撤销会话、阻止旧版本准入，使用有界行锁查询恢复；重启首次准入也核实行锁 | 真实 PostgreSQL 事务边界注入确认丢失、确定回滚、未决提交，以及新 Gate 恢复 |
| R4 | 在 SSH session 建立之前监督上传取消；channel-open 阻塞时可关闭独占 transport | 真实 SSH 对端不接受 channel-open，取消后检查退出和配额回收 |
| R5 | Exec / Upload 保留整个 lease 的所有权；显式 owner 负责 Release / Discard；Cancel / Done 反映实际生命周期 | 连续原语、并发借用、取消、慢 join 与配额断言 |
| R6 | Exec 在查询 / 配额 / 拨号前冻结总预算；Relay 在资源查询前冻结预算，后续步骤只使用剩余时间 | 配额等待、数据库查询和分阶段 deadline 回归 |
| R7 | Relay 每次下游写入 / Flush 收紧写 deadline；取消可中断真实阻塞 I/O | 阻塞初始 Flush、写入、空闲和总超时 |
| R8 | 已写响应头后的 Relay 失败记录结果后中止 HTTP 传输；中间件保留中止语义；usage 将超时存为失败并保留具体原因 | 真实 HTTP 客户端收到截断错误；数据库中的 usage 状态、超时原因和已写 HTTP 200 状态均正确 |
| R9 | 终端所有结束分支先建立清理监督；响应预算与后台资源 owner 分开 | ready 前撤销、阻塞 Close、绝对预算耗尽后保留配额，真正 join 后回收 |
| R10 | 改密提交后的清理不受原 HTTP 请求取消；超时返回明确 503 状态；保留撤销和清理未完成的日志字段 | 请求取消后继续回收；3 秒清理超时；重复 Await 不重置期限；日志序列化保留清理证据 |
| R11 | 配置只能降低 300s / 8MiB / 4MiB 硬上限；执行入口再次限制 | 配置边界、越界和执行 Handler 回归 |
| R12 | M2 显式执行全量 race 与终端协议 E2E；必需 Go 用例必须出现包名和测试符号对应的 pass 事件 | 覆盖表生成和 runner 自测；未选中、跳过及缺失用例阻断门禁 |

用例说明使用英文，机器来源为 `tests/coverage.json`，`tests/coverage.md` 由其生成。

## 验证结果

最终验证于 2026-10-12 完成。原审查的 12 项问题均已修复并补充回归；M2 通过。M1 自动化部分通过，部署人工证据仍为 pending，因此不将 M1 整体标为通过。

| 检查 | 结果 |
| --- | --- |
| `bash tests/run.sh --gate M2` | **通过**：8 个 run；58 个必需实现，其中 57 个 Go 实现均有对应包与测试符号的 pass 事件，另 1 个为前端会话回归 |
| `go-race` | `go test -race ./... -count=1` 通过，使用隔离数据库；包含真实主进程启动 / 重启、事务故障、资源回收和 Relay 回归 |
| `terminal-e2e` | 真实 HTTP / WebSocket / SSH 测试通过，包含 PostgreSQL 与改密 HTTP Handler 的完整撤销链路 |
| `bash tests/run.sh --gate M1` | 5 个自动化 run 通过；7 个必需迁移检查全部通过；`deploy-verify` 因覆盖代码已变化而 pending，门禁正确阻断 |
| 门禁工具 / 覆盖表 | 12 项工具自测通过；`coverage.json` / `coverage.md` 一致，33 cases / 11 runs |
| 前端 | Node 测试通过；`npm run build` 通过 |
| 后端构建 | `go build -buildvcs=false -o <output> ./cmd/server` 通过；本地构建关闭 VCS 版本信息采集 |
| 修改检查 | `git diff --check` 通过；修改的 Go 文件均已格式化 |

最终运行证据：

- [M2 execution record](../tests/.artifacts/exec-mv507b9f-21220.json)
- [M1 execution record](../tests/.artifacts/exec-mv506cak-19724.json)

验证在提交前完成，因此运行记录中的 `commit` 是基线 HEAD，实际测试对象为基线加本次工作区修复。运行证据保存在忽略的 `tests/.artifacts/`，不代替人工部署证据。测试环境为 Go 1.25.0、Node 24.19.0、PostgreSQL 17 / TimescaleDB 2.30.2；测试数据库与业务数据库隔离。

## 验证边界

- 终端协议测试使用真实 HTTP / WebSocket 和受控 SSH 服务。竞态测试使用可控版本源固定执行顺序；完整改密链路另接隔离 PostgreSQL、真实 Auth Handler 与 Service。浏览器终端交互属于后续 M4 验收。
- COMMIT 确认丢失在真实数据库事务的客户端边界注入；不是生产网络故障演练。
- 极端情况下，底层 Close 或 worker 无法及时返回时，前台按期限报告清理未完成；后台唯一 owner 继续持有配额和会话记录，直到资源真正结束。
- M1 的历史部署证据覆盖的代码发生变化，需要重新部署验证；本地自动化结果不能替代该记录。本次未执行部署。
