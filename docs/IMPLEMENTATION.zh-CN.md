# Talus 实施技术方案（IMPLEMENTATION）

> 整理日期：2026-10-09；代码基线：`21d53b4`。
> 配套 [架构文档](ARCHITECTURE.zh-CN.md) 与 [需求文档](REQUIREMENTS.zh-CN.md)。
> 本文只写**实施顺序、工作分解、契约冻结、文件级改动面与验证映射**，不重复需求与架构正文。
> 所有内容待实施；本文不包含已执行的代码修改、迁移或删除。
> 2026-10-09 已根据评审决议更新：A–I 全部给出结论（见 §9），预算/阈值表（§3.5）已冻结。

---

## 0. 如何使用本文

| 文档 | 回答的问题 |
| --- | --- |
| `REQUIREMENTS.zh-CN.md` | 做什么、范围边界、验收标准（TC） |
| `ARCHITECTURE.zh-CN.md` | 现状证据（file:line）与目标结构 |
| 本文 | 按什么顺序、改哪些文件、先冻结哪些契约、怎么验证与回滚 |

工作包（WP）编号规则：`WP-<阶段>.<序号>`；每个 WP 通过 TC ID 回溯需求。本文不新造验收标准，只映射需求 §10 的用例。
状态标记：本文与需求/架构一致；预算、阈值、TC 均已冻结（无遗留“提案”）。“待实施”只表示尚未写代码或未实测，不代表契约未定。

---

## 1. 实施原则

1. **契约先行**：跨需求公共层（SSH lease、JWT gate/registry、统一遥测、图表查询 API）先冻结接口与测试骨架，再各自实现业务。
2. **单一 owner**：一个职责只由一个需求改。典型：`monitor.collectOne` 的部署编排只由 REQ-08 迁移；REQ-06 只交付公共传输/资源层。
3. **加列/建表先于启用**：数据库变更保持向后兼容，先发布加列（`token_version`）与聚合结构，再发代码，最后开启 retention/rollup 调度。
4. **不扩大范围**：不做多 Hub 协调、完整 RBAC、SSH 隧道、零丢失审计、页面采集频率设置。
5. **测试与遥测随功能交付**：未运行的用例登记为 `Planned`，不写成通过；人工用例（读屏）单独标记。
6. **单一提交边界可回滚**：每个 WP 尽量独立成 commit/PR，revert 不牵连其他 WP。
7. **fail-closed**：构建产物、扫描豁免、查询预算缺失或损坏时拒绝启动/拒绝请求，不降级放行。

---

## 2. 依赖图与阶段划分

```text
Phase 1  范围与契约基线
  WP-1.0 四份规划文档落盘入仓库 docs/（需求 / 架构 / 技术方案 / 开发方案）
  WP-1.2 REQ-02 测试组织 + coverage.json（含 scan_exemptions 与机器元数据）
  WP-1.1 REQ-01 适配器移除 + 静态检查（依赖 1.2 的豁免清单）
  WP-1.3 公共契约冻结 ── lease / gate+registry / 遥测 / 图表 API
                │
                ▼
Phase 2  可靠性与会话
  WP-2.1 REQ-06 SSH lease + 上传取消（P0，关键路径）
                │
        ┌───────┴────────┐
        ▼                ▼
  WP-2.2 REQ-07 预算   WP-2.3 REQ-05 JWT 撤销
  (依赖 2.1 的 lease)   (schema/JWT/HTTP 可与 2.2 并行；
                        终端完整回收验收在 2.1+2.2 之后)
                │
                ▼
Phase 3  采集与图表
  WP-3.1 REQ-08 调度/退出
  WP-3.2 REQ-08 Agent 分发（依赖 2.1 传输层）
  WP-3.3 REQ-08 指标（差分/rollup/retention）
  WP-3.4 REQ-04 图表（UI/a11y 可基于 mock 契约并行）
                │
                ▼
Phase 4  WP-4.1 REQ-09 终端 UI（生命周期接入 2.3 的 registry）
```

并行化结论：Phase 1 的 **WP-1.2 先于 WP-1.1**（静态检查需要 `coverage.json` 的豁免清单与机器元数据）；Phase 2 的 2.2 与 2.3 在后端可并行，但 2.3 的终端回收验收必须排在 2.1/2.2 之后；Phase 3 的 3.4 前端可在 3.2/3.3 完成前基于冻结的图表响应契约开发。

---

## 3. 公共契约冻结（Phase 1 必须先完成，`WP-1.3`）

以下接口在 Phase 1 只加定义、错误枚举和测试骨架，不改变现有运行行为。

### 3.1 SSH 资源层 lease（REQ-06/07/08 共用）

目标：把「借用 → 执行/上传 → 归还/丢弃」变成唯一归还权的句柄。

```go
// backend/internal/pkg/sshpool
type Handle struct { /* client, serverID, generation, ... */ }

func (p *Pool) Acquire(ctx context.Context, serverID uint, fp string) (*Handle, error)
func (p *Pool) Release(h *Handle)   // 健康归还，带 generation 校验
func (p *Pool) Discard(h *Handle)   // 损坏丢弃

// backend/internal/service（SSHService）
type Lease struct { /* Handle + 目标快照 + once */ }
func (s *SSHService) AcquireLease(ctx context.Context, serverID uint) (*Lease, error)
func (l *Lease) Exec(ctx context.Context, cmd string, budget Budget) (*ExecResult, *StageError)
func (l *Lease) Upload(ctx context.Context, localPath, remotePath string, budget Budget) *StageError
func (l *Lease) Cancel(reason string)
func (l *Lease) Done() <-chan struct{}
func (l *Lease) Generation() uint64
func (l *Lease) Release() // 幂等，sync.Once
func (l *Lease) Discard()
```

不变式：
- `Release`/`Discard` 恰好一次（`sync.Once`）；第二次调用为 no-op 并记日志，不重复归还。
- generation 不符时**不静默处理**：归还方降级为 `Discard`（关闭该 client）+ 结构化日志 `reason=stale_generation`，避免泄漏或留下死条目（对应架构 §10.5 竞态 1）。
- lease 允许在同一目标快照上复用多个原语（探测→上传→运行），**不在 lease 内部嵌套借用**（需求 §6.1）。
- 错误分类枚举先冻结：`quota / dial / host_key / auth / session / transfer / remote_exit / timeout / canceled / protocol / integrity / permission`，直接对应需求 §12.1 的 `reason_class`。
- **清理预算不是层层重算的 timeout**：调用方传入一个绝对 deadline，lease 内部只读剩余预算（§3.5）。

改动面（Phase 2 才落地）：`sshpool/pool.go`（`GetContext` 返回 generation、`Release/Discard` 加参数）、`service/ssh.go`（`AcquireLease` 替换 `GetClient` 路径）、调用点仅三处：`ssh.go:67`（Exec）、`ssh.go:419`（CopyFile）、`terminal.go:55`。

### 3.2 JWT 用户 gate + 终端 registry（REQ-05）

```go
// backend/internal/service（或 server 中立包）
type UserGate struct{ /* per-userID mutex + version lookup */ }
func (g *UserGate) Admit(ctx context.Context, userID uint, fn func(version int64) error) error

type TerminalSession struct{ SessionID, UserID uint; Version int64; ... }
type TerminalRegistry struct{ /* map[sessionID] -> session */ }
func (r *TerminalRegistry) Register(ctx, s TerminalSession) error   // preparing
func (r *TerminalRegistry) Admit(ctx, sessionID) error              // preparing -> ready
func (r *TerminalRegistry) RevokeUser(ctx, userID, currentVersion)  // 标记 revoked + cancel
func (r *TerminalRegistry) Done(sessionID)                          // 幂等注销
```

- 线性化点是密码事务 `COMMIT`（架构 §7.2、需求 §5.2）。
- 锁序固定：`user gate → DB 行锁` 或 `user gate → registry 锁`；网络 `Close`/`join` 在锁外。
- gate 的 DB 读取与 registry 的时钟/DB 通过接口注入，便于固定时序测试（TC-05-04/05）。
- 版本查询**每次走主库**，禁止正向缓存；健康性能目标见 §3.5。

### 3.3 统一遥测契约（REQ-05/06/07/08）

- 日志字段固定为需求 §12.1 的 schema（`schema_version=1`、`component`/`stage`/`outcome`/`reason_class` 等枚举）。
- 新增进程内指标注册表，暴露需求 §12.2 的 11 个指标；标签只允许固定枚举，ID/主机/版本/hash/预算数值只进日志。
- **lease Gauge owner 唯一（已决议）**：部署与采集链路统一 `consumer=agent`，**移除 `monitor` 标签**；仅 SSH 资源层更新该 Gauge——占用配额时 `+1`，归还或丢弃时幂等 `-1`；各部署阶段不重复计数。最终 `consumer ∈ {exec, terminal, agent}`。
- 每个逻辑操作「最终事件恰好一次」「Counter 恰好一次」「Gauge 增减对应唯一 owner」；提供测试快照读取接口供 TC 断言。
- 先实现注册表 + 枚举校验 + 一个 demo 埋点（建议 `ssh.upload`）验证契约，再全面接线。

### 3.4 图表查询 API 契约（REQ-04/08）

```text
GET /api/v1/servers/{id}/metrics
  ?range=15m|1h|6h|24h|7d|30d
  &interval=auto|1m|5m|15m|1h|6h        // 统一字段名 interval
  &stat=avg|max
  &allow_coarsen=true|false             // 显式 interval 默认 strict
  &from=<RFC3339|unix_seconds>&to=<RFC3339|unix_seconds>   // 精确边界，服务端不改写
响应 data: {
  range, requested_interval, actual_interval,
  coarsen_reason: null|"bucket_limit"|"raw_budget"|"rollup_required",
  stat, downsampled: bool, from, to,
  sampled_at, coverage_seconds, source: raw|rollup|mixed,
  points: [{ t, <series>: number|null, ... }]
}
```

- 前端可基于该 mock 契约先行开发（`WP-3.4`），后端在 `WP-3.3` 落地。**字段改名须在同一变更内完成 decoder 迁移**：旧的 `bucket`/`allow_downsample`/`actual_bucket` → `interval`/`allow_coarsen`/`actual_interval`，同步更新 `types/metrics.ts` 与前端测试，禁止新旧字段并存。
- `null` 语义为「无有效采样」，与真实 `0` 区分；峰值由桶内已采样 max 保留（需求 §4.2）。
- **查询保护（已决议）**：
  - 上限：`600` 桶、`12,000` 个 raw 输入样本、查询阶段总计 `2s`（服务端）。
  - 显式 `interval` 默认 strict：超预算返回 `422` `metrics_query_budget_exceeded` 并附 `recommended_interval`；`interval=auto` 或 `allow_coarsen=true` 才降到 6h，并回填 `coarsen_reason`。
  - rollup 未就绪且 raw 回退超预算，或已是 6h 仍缺物化 → `503` `metrics_rollup_not_ready`；查询 deadline 到期 → `503` 并标明超时；非法 `interval`/`range>30d` → `400`，**绝不缩短 from/to**。禁止先放大 bucket 再全扫 raw。
  - 降级时 `downsampled=true` 且 `actual_interval` 可见提示。
  - `12,000` 是计划输入预算，实际数据库扫描量在 `WP-3.3` 用 EXPLAIN 实测验证。

### 3.5 预算与阈值（已冻结）

**清理预算（已决议）**——`grace` 是优雅关闭阶段；强制关闭与 `join` 使用同一个总预算的剩余部分；多层调用共享绝对 deadline，不得重置计时。

| 配置 | 值 | 说明 |
| --- | --- | --- |
| `LOCAL_CLEANUP_BUDGET` | `3s` | 本地清理总预算（绝对截止），向下传递 |
| `SSH_TEARDOWN_GRACE` | `100ms` | Exec / Agent SSH / Relay 的优雅关闭阶段 |
| `TERMINAL_TEARDOWN_GRACE` | `2s` | 终端优雅关闭阶段（与 REQ-05 一致） |
| 强制关闭 + join | 剩余预算 | 例：终端 2s 优雅 + ≤1s 强制/join；Exec 100ms 优雅 + ≤2.9s 强制/join |

**认证性能（已决议）**：gate + 连接池等待 + 主库版本查询合计 `p95 ≤ 20ms`、`p99 ≤ 50ms`（健康目标，测试资源与流量条件写入文档）；`2s` 是故障截止预算，不作为正常性能目标。当前未实测。

**其余阈值**：

| 项目 | 值 | 状态 |
| --- | --- | --- |
| `SSH_QUOTA_WAIT` / `SSH_DIAL_TIMEOUT` | 10s / 10s（现状） | 已定 |
| `EXEC_TIMEOUT` 默认 / 上限 | 30s / 300s（现状，改为读配置而非 Handler 硬编码） | 已定 |
| `EXEC_OUTPUT_LIMIT` 总 / 单流 | 8 MiB / 4 MiB（需求 §7.1.2） | 已冻结 |
| Relay 普通 / 受限长流 | standard 30s / bounded_stream 300s；header 与 idle 均 30s（需求 §7.1.3） | 已冻结 |
| `MONITOR_INTERVAL` | 60s，新增 `>0` 校验 | 已定 |
| 全局采集并发 / 30s deadline / backoff | min(N,16) / 30s / base=min(I×2^(k−1),max(I,300s)) ±10% jitter（需求 §8.1.1） | 已冻结 |
| auth 主库查询 deadline | 2s（故障截止） | 已决议 |
| metrics raw / 6h rollup 保留 | 35d / 35d（需求 §8.3.1） | 已定 |

---

## 4. Phase 1：范围与契约基线

### WP-1.0 设计文档落盘

- 将 `ARCHITECTURE.zh-CN.md`、`REQUIREMENTS.zh-CN.md`、`IMPLEMENTATION.zh-CN.md`、`DEVELOPMENT-PLAN.zh-CN.md` 放入仓库 `docs/` 并纳入版本控制（与 `docs/README.zh-CN.md` 同目录）。
- 状态：四份文档已由本会话写入 `docs/`，`git status --short` 显示为未跟踪（`??`），需随首个变更提交；相互链接已成立（需求 ↔ 架构 ↔ 技术方案 ↔ 开发方案）。
- `.pi/docs/` 下的其他本地草稿（dokploy/portainer 说明、release notes、前端修改方案等）仍在 gitignored 的 `.pi/`，不进入仓库；本方案不依赖它们。

### WP-1.2 REQ-02 测试组织与统一入口（先于 WP-1.1）

- 新增 `tests/`（`README.md`、`cases/*.md`、`coverage.json`、`coverage.md`（生成物）、`render-coverage.mjs`、`fixtures/`、`e2e/`、`integration/`、`static/`、`run.sh`）。
- **`tests/coverage.json` 是唯一机器源（已决议，需求 §3.2.1）**：
  - 顶层 `schema_version=1`、`runs`、`cases`、`scan_exemptions`；`runs[].suite ∈ {fast, integration, ui, e2e}`，`runner ∈ {go-test, node-test, playwright, shell, manual}`，`command` 为 argv 数组；case/run/implementation 引用唯一。
  - `tests/coverage.md` 由 `node tests/render-coverage.mjs` 确定性生成，只读、禁止手改；`--check` 差异返回非零，CI 不允许两份元数据分叉。
  - `scan_exemptions`: `[{ path, kind, reason }]`，**英文 reason**、精确路径（仅专用负例允许目录前缀）；`docs/` 下四份规划文档逐文件登记；**不得整体排除 `docs/**`/`tests/**`**；不得绕过「适配器目录不存在」的断言。
  - §10 用例表只作英文场景摘要，`Test level` 不决定 suite；`run.sh --suite` 只读 `coverage.json`。
  - 静态定义 vs 运行期：`coverage.json` 的 `runs[]`/`run_id` 是**静态运行定义**，也是去重键；某次执行另记 `execution_id`（含 commit/时间/环境/结果），结果文件写 `tests/.artifacts/exec-<execution_id>.json`，按 `run_id` + `implementation` 对齐 case。**不要把 `run_id` 当运行期 ID。**
- `run.sh` 按 `--suite fast|integration|ui|e2e|all` 分派，**先校验 coverage.json schema**，再 `cd` 到 `backend`/`frontend` 调各自 runner；缺依赖显式失败；子 runner 非零向上传播。
- **fail-closed**：`coverage.json`、其 schema 或 `scan_exemptions` 缺失/损坏时，`fast`/`all` 必须失败。
- CI 增加 `tests/run.sh --suite fast` 必跑。

验证：TC-02-01/02；根目录运行能定位工作目录；mock UI 与真实 E2E 区分。
风险：`tests/cases/*.md` 是说明，必须与 `coverage.md` 的状态一致，避免「只有目录名」。

### WP-1.1 REQ-01 移除适配器 + 静态检查（TC-01-01/02/03）

改动面：
- 删除 `ai-integration/`（`pi/`、`opencode/`、`claude/`、`codex/`、`install.sh`、`README.md`）。
- `README.md`、`docs/README.zh-CN.md`：移除「安装服务目录注入插件」、适配器下载、自动注入、hook 缓存；保留 skill 安装、`TALUS_URL/TALUS_API_KEY`、API、Relay。
- `skills/talus/SKILL.md:158` 附近：移除指向 `ai-integration/` 的说明，保留核心流程。
- 新增 `tests/static/adapters-removed.sh`（行为见需求 §2.3）；`tests/run.sh` 的 `fast` 与 `all` 调用它。
- **豁免来源**：读取 `coverage.json` 的 `scan_exemptions`；`docs/` 下四份规划文档逐文件登记（英文 reason）。检查器在豁免文件缺失时失败。

验证：`bash tests/run.sh --suite fast` 通过；TC-01-01/02/03。
回滚：单 commit revert 即恢复目录与文档。

### WP-1.3 公共契约冻结

- 按 §3 落接口、枚举与测试骨架；不含业务实现。
- 认证健康基准（§3.5 p95/p99）在此实测记录。
- 产出：lease/gate/registry/遥测/图表契约的 Go 接口与 mock、遥测注册表、图表 mock 响应。

### WP-1.4 迁移框架硬化（首个新迁移的前置）

- 背景：`ApplyOnce` 现为“检查 → SQL → marker”分步、无事务/锁（架构 §10.4 P1）；`users.token_version`、`metrics_rollup_6h` 等新迁移必须建立在它之上，不能把这些新迁移放到 WP-3.3 才硬化。
- 改动面：`backend/internal/repository/migrations.go`（owner：repository 层）。
- 步骤：
  1. migration ID 锁 + 原子事务：取得锁→检查→执行 SQL→写 marker 在同一事务提交；中断/失败不留下部分 marker。
  2. 失败可安全重跑（幂等）；重复/并发执行不双重应用。
  3. 验收：中断、重跑、并发竞争三类用例；确认后 `users.token_version`（WP-2.3）与 rollup 建表（WP-3.3）才可开始。
- DoD：迁移硬化用例通过；所有后续新迁移都经它。
- 回滚：保留旧 marker 语义，可回退到分步实现（仅限故障隔离）。

---

## 5. Phase 2：可靠性与会话

### WP-2.1 REQ-06 SSH lease 与上传取消（P0，关键路径，TC-06-01/02/03）

实施步骤：
1. `sshpool/pool.go`：条目增加 `generation`；`Acquire/Release/Discard` 校验 generation；空闲清理不驱逐仍持有配额的条目。
2. `service/ssh.go`：以 `Lease` 重写 `GetClient` 路径；`Exec` 与 `CopyFile` 改为基于 lease；lease 内 `sync.Once` 保证恰好一次归还。
3. 上传路径（现 `CopyFile`，`ssh.go:418-457`）：传入绝对 `LOCAL_CLEANUP_BUDGET`，加总 deadline、响应父 `context`、显式 `join` worker（对齐 `runCommand` 的取消监督）。
4. Gauge：仅在资源层计入 `consumer=agent`（§3.3）。
5. 测试：SSH 协议模拟对端 + 固定并发时序 + `-race`；覆盖成功、源文件失败、session 失败、传输中断、阻塞对端、父取消、重复上传后配额可用。

验证：TC-06-01/02/03；连续上传超过 3 个配额后 Exec/Terminal/采集仍可借用；Gauge 回到基线。
风险：`Release` 签名变化影响 `terminal.go:55` 与 `Exec`；一次性替换调用点并跑全量 race。

### WP-2.2 REQ-07 统一超时预算（TC-07-01/02/03/04）

实施步骤：
1. `config/config.go`：新增 §3.5 配置键 + `Validate()`（含 `MONITOR_INTERVAL>0`）。
2. `handler/exec.go`：移除硬编码 30/300，改用 config；请求 `timeout` 覆盖逻辑按「请求值 → EXEC_TIMEOUT → 上限」。
3. `cmd/server/main.go`：`WriteTimeout` 不再全局 15s 截断长路由；对长 Exec/Relay 用 `http.NewResponseController(w).SetWriteDeadline` 或路由级适配。**注意**：流式响应须在每次写前重新 arm deadline，否则长流仍会被截断。
4. 清理 deadline：调用方构造一次绝对截止（`LOCAL_CLEANUP_BUDGET=3s`），grace 为 100ms（§3.5）；禁止多层重算。
5. `service_relay.go`：普通请求与受限长流分开；写入失败/上游超时/客户端取消可识别；保留 CRUD 保护。
6. 输出上限：达到限额后**继续读取并以常量空间丢弃**（不用 `io.LimitReader` 截断，避免输出背压阻塞命令），直到真实退出或原 deadline；仅实际丢弃时置 `output_truncated/stdout_truncated/stderr_truncated`（恰好到限随 EOF 不算）；返回各流 retained/discarded 与真实退出码，截断为独立元数据。
7. 反向代理 timeout 写入部署文档。

验证：TC-07-01（20–30s Exec 完整返回、默认生效、超上限一致）、TC-07-02/03/04。
风险：写 deadline 调整影响所有路由；先在长路由试点，普通 CRUD 保持 15s。

### WP-2.3 REQ-05 JWT 撤销与终端回收（TC-05-01..05）

实施步骤：
1. 迁移：`users` 加 `token_version bigint not null default 0`（经 `ApplyOnce`，由 WP-1.4 前置）；`token/jwt.go` 加 claim；`middleware/auth.go` 每请求查主库版本（禁正向缓存）。
2. `service/auth.go ChangePassword`（`auth.go:132`）：事务内锁行、校验原密码、递增版本；COMMIT 为线性化点。
3. 未决 COMMIT：COMMIT 报错且结果未知时，在 gate 下标记该用户提交未决、取消 JWT 会话并返回可区分的 503；用**有界**主库行锁/事务状态核实原事务最终结果（单次普通查询读到旧版本不足以证明回滚），确认后解除未决；超时不无限持 gate。
4. 重启恢复：Hub 启动后该用户 gate 的首次准入同样通过有界主库加锁读取排除上次未决密码事务，不允许靠进程重启直接放行。
5. `UserGate` + `TerminalRegistry`；`service/terminal.go` / `handler/terminal.go` 接入 preparing→ready 与 revoke（终端 grace 2s，总 3s）。
6. 前端 `lib/auth.ts`：401 清理认证与敏感缓存，跨标签同步，回登录页。
7. 测试：固定时序 + 未决提交、重启恢复、清理超预算分别断言。
5. 测试：固定时序暂停在初次校验后/登记后/ready 前并发改密码；主库失败、回滚、结果未知、清理超时分别断言。

验证：TC-05-01..05；旧 JWT 401、旧 WS 关闭、配额回收、API Key 不受影响、历史无 claim JWT 失效。
风险：每请求主库版本查询的延迟；`WP-1.3` 记录 p95/p99 基准（§3.5），超目标先回设计。

---

## 6. Phase 3：采集与图表

### WP-3.1 REQ-08 调度与退出（TC-08-01）

- `main.go:284` 起：Monitor 接入 App 根 context；退出时停止调度、取消在途、等待回收，再关连接/存储。
- `monitor.go`：独立调度、全局并发上限、每节点防重入、总 deadline、失败 backoff；慢节点不阻塞整批。
- `service/server.go:38`：`statusThreshold` 由 `MONITOR_INTERVAL`、容错轮数、允许延迟推导，去掉固定 120s。
- `config.Validate`：启动前拒绝非法 `MONITOR_INTERVAL`。
- **调度数值（已冻结，需求 §8.1.1）**：min(N,16) 在途；每节点 ≤1 在途 + 1 待调度；准入后整体 30s deadline；退避 base=min(I×2^(k−1), cap)，cap=max(I,300s)，±10% jitter，clamp 到 [I,cap]，从清理完成计时；成功清零、不补跑；退出/删除/generation 取消不累计失败。TC-08-01 断言零抖动 I=60s → 60/120/240/300/300s。
- 遥测：接入 §3.3 的 `monitor.collect` 事件与指标（成功/失败/超时/漏采/延迟）。

### WP-3.2 REQ-08 Agent 分发（TC-08-02/04/05/08）

- 新增 `backend/build/agents.sh`：交叉编译 linux/amd64 与 arm64，生成 manifest（`platform / build version / protocol_version / SHA-256 / size`）。
- 改根 `Dockerfile` 与 `backend/Dockerfile`：复用该脚本，仓库根为 build context，两种 Hub 镜像都携带两份 Agent + manifest。
- **启动校验 fail-fast（已决议）**：本地清单或必需产物缺失/损坏/不兼容时非零退出，不启动 HTTP/Monitor；这是发布包错误。远端某节点的权限、架构或协议错误只影响该节点采集。
- 新增 `AgentDeploymentService`：唯一 owner，接受 context/serverID，返回采集输出与产物身份；`monitor.collectOne` 的部署逻辑迁入。
- 探测：一条复合命令同时取 `uname -s`、`uname -m`；映射 x86_64→amd64、aarch64→arm64；探测预算 5s，失败上传次数为 0。
- **协议契约（已决议）**：采集 JSON 必须携带 `agent_version` 与整数 `protocol_version`；首版 `protocol_version=1`，**精确匹配**；`agent_version` 必须与 manifest 的构建版本一致；**hash 只验证产物完整性，不代表协议**。
- 部署检查顺序：远端 hash/manifest 比对 → 必要则上传校验 → 原子替换 → 运行 → 校验输出协议 → 入库。缺字段的旧输出按 `legacy` 拒绝入库（先升级 Agent 再采集）；`protocol` 不匹配拒绝入库且不写部分指标。
- CI：TC-08-05 检查两种镜像内部 ELF 架构、size/hash、协议烟测。

### WP-3.3 REQ-08 指标与保留（TC-08-03/06）

- `repository/metric.go`：网络/磁盘先按相邻样本 `delta/time`，再按图表桶聚合；查询起点取前驱样本；reset/负时间差/超断采阈值不参与、返回未知并成缺口；单独保留已采样区间峰值。
- **rollup 实现（已决议）**：首期自维护 `metrics_rollup_6h` 表 + 持久化检查点；**桶结果与完成检查点同事务提交**，统一处理相邻差分、有效时长权重、空桶与故障补齐。后续有实测依据再评估 continuous aggregate。
  - 并发安全：桶以 `(server_id, bucket_start)` 唯一定位（**每桶一行**保存统计状态，`stat` 是查询选项，不是键的一部分）；upsert + advisory lock 防并发重复；检查点覆盖「已确认空桶」。
- raw 35d、rollup 35d；每小时重算最近 48h（含首次 35d 回填）；每天按完整 chunk 清理，删前确认聚合完成，失败暂停删除并告警。
- 查询拼接：完整已物化桶走 rollup，两端/未闭合/未物化桶按边界从 raw 算，`[start,end)` 互斥。
- 容量实测：记录 1d/7d 表/索引/chunk 大小，外推 35d（需求 §8.3.2）。

### WP-3.4 REQ-04 图表尺度、交互与可访问性（TC-04-01..05）

- 前端：`types/metrics.ts`、`features/monitoring/components/time-range-selector.tsx`（加 15m/30d）、`trend-chart.tsx`、`lib/series.ts`、`hooks/use-metrics.ts`；新增数据替代视图与键盘/读屏支持（需求 §4.5）。
- 后端：metrics handler 按 §3.4 契约返回 `actual_interval`/`coarsen_reason`/`stat`/`coverage`/`source`/`downsampled`/`null`，并执行 §3.4 查询保护（600 桶 / 12,000 样本 / 2s；422 或 503）。
- 峰值：`stat=max` 返回桶内已采样 max，长窗降采样保留该信息。
- 交互：切换范围取消旧请求/隔离迟到；后台页暂停、可见补拉；375px 与键盘。
- 可访问性：非纯颜色辨识、对比度、焦点、读屏（人工用例单独标记）。

---

## 7. Phase 4：REQ-09 终端 UI（TC-09-01/02）

- `frontend/src/features/terminal/*`：顶栏信息/状态/重试计数、手动连接控制、全屏与布局、字号与主题记忆、xterm 官方搜索、剪贴板与清屏、fit + 防抖 PTY 尺寸、移动端 375px/44px。
- 生命周期接入 `WP-2.3` 的 registry：切主机/连续重连/卸载/撤销后无重复 WS/handler/timer，token 不放 URL，断连回收配额。
- 首期单连接工作区；多标签/分屏/布局恢复不做。

---

## 8. 数据库迁移与发布

| 变更 | 迁移方式 | 顺序 | 回滚/恢复 |
| --- | --- | --- | --- |
| `users.token_version` | 走 `migrations.go ApplyOnce`（带 migration ID），**不用 `AutoMigrate`**；加列 `bigint not null default 0` | 先于 REQ-05 代码；旧二进制读新列不受影响 | 保留列不删，回滚代码即可，无降版本 DDL |
| `metrics_rollup_6h` + 检查点 | `ApplyOnce` 建表/唯一约束/检查点表 | 先于 retention 启用 | 停调度并保留结构；确认无查询依赖后再移除 |
| retention/rollup 调度 | 代码开关，默认关闭 → dry-run → 开启 | 最后 | 关闭开关即停；已删 raw **不可逆**，故 dry-run 与「删前确认聚合完成」是硬前置 |
| `migrations.go ApplyOnce`（事务 + migration ID 锁 + 失败重试） | **所有新迁移的前置任务（WP-1.4）**，owner 为 repository 层 | 先于 `users.token_version` 与 rollup 建表 | 迁移幂等；失败可安全重跑；中断不留下部分 marker |

- 在隔离 TimescaleDB 验证新库与历史升级路径，不以外键关闭的仓储测试代替完整迁移验证。
- **首次启用撤销机制：不得滚动共存**——先停止旧 Hub 并结束旧 WS 会话，再启动带版本校验的新构建；不接受“发布窗口内旧副本放行旧 JWT”。后续发布与回滚均保留版本校验与主动撤销，不存在允许无版本校验副本并行服务的窗口。
- **fail-fast 位置**：Agent manifest/产物校验须在绑定 HTTP 端口之前完成（§WP-3.2）。
- **回滚**：`token_version` 与 rollup/retention 结构保留即可回滚代码；但**回滚后的旧二进制仍须保留版本校验与旧 JWT 主动撤销**，否则会恢复已被撤销的会话——因此禁止回滚到无版本校验的构建；唯一不可逆操作是 raw 清理。

---

## 9. 评审决议与残留风险（A–I）

| # | 决议（已确认） | 残留动作 / 注意 |
| --- | --- | --- |
| A | §10 只作英文场景摘要；`Test level` 不决定 suite。机器元数据放 `tests/coverage.json`（唯一机器源），`coverage.md` 由 `render-coverage.mjs` 生成。 | `coverage.json` 的 `run_id` 是静态运行定义与去重键；运行期用独立 `execution_id`（写 `exec-<execution_id>.json`）。清单/豁免缺失或报告 drift 时 checker 必须 fail-closed。 |
| B | 采集 JSON 带 `agent_version` + 整数 `protocol_version`；首版=1 精确匹配；`agent_version` 匹配 manifest；hash 只做完整性。旧输出按 `legacy` 拒绝入库。 | 明确部署检查顺序（远端 hash → 必要时上传 → 运行 → 校验协议），使 legacy 成为安全网而非常态；`protocol` 不匹配拒绝入库且不写部分指标；无法升级的节点表现为持续分类失败，不静默。 |
| C | 部署与采集统一 `consumer=agent`，移除 `monitor`。仅 SSH 资源层更新 lease Gauge（占用 +1 / 归还丢弃幂等 -1）。 | 更新需求 §12.2 标签枚举；该上限与 `min(节点,16)` 并发上限、其它 lease 消费者一起纳入预算。 |
| D | 首期自维护 `metrics_rollup_6h` + 持久化检查点，桶与检查点同事务提交。 | 唯一约束 + upsert + advisory lock 防并发重复；检查点粒度覆盖空桶；后续再评估 continuous aggregate。 |
| E | 首期 fail-fast：本地清单/必需产物缺失、损坏、不兼容 → 非零退出，不启动 HTTP/Monitor。远端错误只影响该节点。 | 校验须在监听端口前完成；CI 保证两种镜像都含两份 Agent。 |
| F | 健康基准 p95 ≤ 20ms、p99 ≤ 50ms（gate + 池等待 + 版本查询）；2s 是故障截止，不是正常目标。 | 目前未实测；`WP-1.3` 记录基准与测试资源/流量条件。 |
| G | 查询保护：600 桶 / 12,000 raw 样本 / 2s。30d 手选 1m/5m/15m/1h 均超点预算：允许降粒度→6h，否则 422；rollup 未就绪且 raw 超预算→503，禁止放大 bucket 后全扫 raw。 | `12,000` 是计划输入预算，实际扫描量需 EXPLAIN 实测；首部署 rollup 未就绪时 30d 返回 503 属预期，需在 UI/文档说明。 |
| H | 豁免清单统一在 `tests/coverage.json` 的 `scan_exemptions`（精确路径 + 英文理由）；四份规划文档在 `docs/` 逐文件登记（随仓库提交，干净 CI 可读）；禁止整体排除 `docs/**`；豁免只影响内容扫描。 | checker fail-closed；REQ-01 静态检查依赖 REQ-02 的 `coverage.json`，故 `WP-1.2` 先建骨架。 |
| I | 架构 §7.1 已改为 `owner_binding_version`（历史 Key 所属用户绑定回填标记）；它不参与 JWT 撤销；`users.token_version` 才管 JWT 会话。 | 仅文档修正；代码中 `APIKey.OwnerBindingVersion` 已是该语义（`BeforeCreate` 置 1）。 |

---

## 10. 完成标准与验证入口

| 阶段 | 完成标准 |
| --- | --- |
| Phase 1 | 四份规划文档纳入版本控制；`tests/run.sh --suite fast` 通过（含适配器检查与 fail-closed 豁免）；公共契约骨架合入；迁移框架（WP-1.4）硬化 |
| Phase 2 | TC-06-01/02/03、TC-07-01..04、TC-05-01..05 通过；race 检查通过；长 Exec 完整返回；旧 JWT 与其终端不可用；清理在 3s 绝对预算内 |
| Phase 3 | TC-08-01..08、TC-04-01..05 通过（读屏人工项单独标记）；双 Agent 镜像校验通过；35d/6h rollup 可核验；查询保护 422/503 生效 |
| Phase 4 | TC-09-01/02 通过；375px/横屏可用；撤销后无残留 WS/handler/timer |

统一入口：`bash tests/run.sh --suite <fast|integration|ui|e2e|all>`；`all` 缺必需检查返回非零。
