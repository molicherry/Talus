# Talus 开发方案（DEVELOPMENT PLAN）

> 制定日期：2026-10-09；代码基线：`21d53b4`。
> 依据 [需求文档](REQUIREMENTS.zh-CN.md) 与 [架构文档](ARCHITECTURE.zh-CN.md)，落实 [实施技术方案](IMPLEMENTATION.zh-CN.md) 的契约与预算。
> 本文是**任务级可执行计划**：把每个 REQ 拆成带依赖、改动面、步骤、TC 映射与完成定义（DoD）的任务。所有任务待实施。
> 与另两份文档的分工：需求定“做什么/验收”，架构定“现状与目标”，技术方案定“契约与预算”，本文定“谁在什么顺序、动哪些文件、何时算完成”。

---

## 0. 阅读约定

- 任务号 `T<阶段>.<序号>`；每个任务标注 `REQ`、`TC`、前置、改动面、步骤、DoD、回滚。
- 阶段门禁（Gate）不通过不得进入下一阶段；P0（含 T2.1）优先于并行任务。
- 复用既有文档的编号：契约细节见 `IMPLEMENTATION.zh-CN.md` §3，冻结数值见需求 §7.1/§8.1.1，用例见需求 §10。
- 无“候选值”：预算/阈值一律取自需求，不在实现里另定默认。

## 1. 目标与范围

- 交付 REQ-01/02/04/05/06/07/08/09 的首期范围；不做多 Hub、完整 RBAC、SSH 隧道、零丢失审计、页面采集频率设置。
- 每个任务必须同时交付：业务行为 + 对应 TC 用例 + 文档更新 + 必要迁移/升级说明。
- 保持模块化单体（React + Go + PostgreSQL/TimescaleDB + xterm），不引入新的测试/监控平台。

## 2. 阶段、里程碑与门禁

| 阶段 | 任务 | 里程碑 | 门禁（全部满足才通过） |
| --- | --- | --- | --- |
| Phase 1 基线与契约 | T1.1–T1.6 | **M1 契约冻结** | 按预登记的 Phase 1 集合（见「阶段测试范围」）：`fast` + T1.6 隔离数据库子集通过；适配器在 T1.4 后真实移除且检查器 fail-closed；coverage.json + 生成器可用；四类契约有接口与骨架。**本阶段 required 项未实现或缺证据即阻断 M1**；仅后续阶段 required 项可暂不计入 |
| Phase 2 可靠性与会话 | T2.1–T2.6 | **M2 可靠性回归** | TC-06-01..03、TC-07-01..04、TC-05-01..05 通过；race 检查通过；3s 绝对清理预算可断言 |
| Phase 3 采集与图表 | T3.1–T3.6 | **M3 采集/图表验收** | TC-08-01..08、TC-04-01..05 通过（读屏人工项单独标记）；双 Agent 镜像校验通过；35d/6h rollup 可核验 |
| Phase 4 终端体验 | T4.1–T4.2 | **M4 终端验收** | TC-09-01/02 通过；375px/横屏可用；撤销后无残留 WS/handler/timer |

关键路径：`T1.2 → T1.3 → T1.5 → T2.1 → T2.4 → T2.2 → T2.6`；**T2.4（HTTP 写 deadline 路由化）必须先于 T2.2/T2.3**，否则长 Exec/Relay 仍被 15s 全局写超时截断；**T1.6（迁移硬化）必须先于 T2.5**（首个新迁移）；Phase 3 的 `T3.6` 前端可基于冻结 mock 契约提前并行。

**阶段测试范围（按阶段预先登记集合选择，不按实现状态筛选）**：每阶段在 coverage.json 中**预先登记**该门禁必须通过的 run/case 集合；本阶段 required 项无论是否实现都必须纳入——未实现或缺证据即**阻断门禁**；只有后续阶段的 required 项可从本门禁排除。
- M1：`fast`（adapters-removed + coverage 工具用例 + T1.5/T1.6 骨架编译/单测）**加 T1.6 的隔离数据库子集**（迁移事务/锁/中断/并发，只从 `integration` 选这些 run）；不含其余 integration、ui、e2e。
- M2：`fast` + `integration`（撤销/迁移）+ 约定 e2e 终端链路；TC-05/06/07 全集。
- M3：`integration`（镜像/迁移/Timescale）+ `ui`（图表）+ `fast`；TC-04/08 全集。
- M4：`ui`（终端）+ 约定 e2e；TC-09。

## 3. 依赖与并行关系

```text
T1.1 ─┬─ T1.2 ─ T1.3 ─ T1.4
      ├─ T1.5(公共契约) ─┬─ T2.1 ─ T2.4 ─┬─ T2.2
      │                  │              └─ T2.3
      │                  └─ T3.1 ─ T3.2 ─ T3.3 ─ T3.4 ─ T3.5 ─ T3.6
      └─ T1.6(迁移硬化) ─ T2.5 ─ T2.6 (T2.6 还需 T2.1 与 T2.2)
T2.6 ─ T4.1 ─ T4.2
```

- 同一文件的写者唯一：`monitor.collectOne` 的部署编排只由 T3.3 迁移；REQ-01 文档清理只由 T1.4。
- `T2.1` 冻结的 lease 原语是 `T2.2/T2.3/T2.6/T3.3` 的共同前置。

---

## 4. 任务分解

### Phase 1 基线与契约

#### T1.1 设计文档与豁免登记（已完成部分，收尾）
- REQ：REQ-02（文档落点）；TC：—
- 前置：无
- 改动面：`docs/{ARCHITECTURE,REQUIREMENTS,IMPLEMENTATION,DEVELOPMENT-PLAN}.zh-CN.md`、`tests/coverage.json`（登记的 `scan_exemptions`）
- 步骤：
  1. 四份规划文档放入仓库 `docs/` 并纳入版本控制，随同一变更提交（干净 CI 可读到豁免清单引用的文件）。
  2. 在 coverage.json 的 `scan_exemptions` 逐文件登记：ARCHITECTURE/REQUIREMENTS/IMPLEMENTATION/DEVELOPMENT-PLAN（`kind=planning`，英文 reason）。
  3. 更新需求 §2.3 的初始登记清单为四份。
- DoD：四份文档存在、相互链接可解析；豁免清单包含全部四份且无 `docs/**` 通配。
- 回滚：文档移动即可，无代码影响。

#### T1.2 测试组织骨架与统一入口
- REQ：REQ-02；TC：TC-02-01、TC-02-02
- 前置：T1.1
- 改动面：新增 `tests/README.md`、`tests/cases/*.md`、`tests/coverage.json`、`tests/render-coverage.mjs`、`tests/run.sh`、`tests/fixtures/`、`tests/e2e/`、`tests/integration/`、`tests/static/`；CI 工作流
- 步骤：
  1. 定义 `coverage.json` schema（顶层 `schema_version=1`、`runs`、`cases`、`scan_exemptions`）与校验器（go-test/node-test/playwright/shell/manual runner 枚举、argv、cwd、timeout）。`run_id` 是**静态运行定义与去重键**；某次执行另记 `execution_id`，结果写 `tests/.artifacts/exec-<execution_id>.json`，不要把 `run_id` 当运行期 ID。
  2. 实现 `render-coverage.mjs` 与 `--check`（生成 `coverage.md`，drift 非零）。
  3. `run.sh`：先校验 schema，再按 `--suite fast|integration|ui|e2e|all` 调各 runner，子 runner 非零向上传播；`fast` 缺必需文件即失败（fail-closed）。
  4. 把现有 Go/前端测试登记进 coverage.json（先登记、不改实现）；mock UI 与真实 E2E 分列。
  5. CI 加 `bash tests/run.sh --suite fast` 必跑 + `node tests/render-coverage.mjs --check`。
- DoD：TC-02-01/02 通过；`coverage.md` 与 json 无 drift；从仓库根运行能定位工作目录。
- 回滚：删除 `tests/` 与 CI 步骤。

#### T1.3 适配器移除静态检查
- REQ：REQ-01；TC：TC-01-01、TC-01-03
- 前置：T1.2（需要豁免清单）
- 改动面：新增 `tests/static/adapters-removed.sh`、`tests/fixtures/adapters-removal/`；`tests/run.sh` 的 fast/all 调用
- 步骤：
  1. 断言 `ai-integration/` 不存在（空目录、悬空软链也算残留）。
  2. `rg --files --hidden --no-ignore` 建清单；内容规则查安装器名、`<service-skills-directory>`、旧“安装服务目录注入插件”等；泛词（Codex/OpenCode/skill/plugin/hook）不禁。
  3. 内容豁免只读 coverage.json 的 `scan_exemptions`；区分 `rg` 返回码 0/1/2；缺 `rg`/必需文件/读取失败均失败；不以 `|| true` 吞错。
  4. 正向断言 skill 与两份 README 的安装/API 说明保留。
  5. 负例 fixture：注入残留、空目录/软链、旧安装 URL、缺失 skill、扫描错误 → 检查器必须拒绝。
- DoD（本任务）：检查器对 fixtures/负例的行为正确（TC-01-01 的检查器部分 + TC-01-03 通过），总返回码语义 0/1/2。**真实仓库的“适配器已移除”断言不在本任务验收**，由 T1.4 完成后在 M1 验收。
- 回滚：删除脚本与 fixture。

#### T1.4 适配器与文档清理
- REQ：REQ-01；TC：TC-01-02
- 前置：T1.3（检查器先就绪，才能证明清理有效）
- 改动面：删除 `ai-integration/`（pi/opencode/claude/codex、install.sh、README.md）；`README.md`、`docs/README.zh-CN.md`；`skills/talus/SKILL.md:158` 附近；目录树/架构引用
- 步骤：
  1. 删除 `ai-integration/` 全目录。
  2. 两份 README 移除安装/注入/hook 缓存说明，保留 skill 安装、`TALUS_URL/TALUS_API_KEY`、API、Relay。
  3. 清理 SKILL.md 中指向已删除适配器的说明。
  4. 同步架构/需求中的目录树与引用（保留历史记录，不新增适配器）。
- DoD：删除 `ai-integration/` 后检查器对**真实仓库**通过（TC-01-01 的真实移除断言在此验收）；TC-01-02/03 通过；skill 与服务目录/Relay 能力无回归。
- 回滚：单 commit revert。

#### T1.5 公共契约冻结
- REQ：REQ-05/06/07/08；TC：TC-05-04/05、TC-06-01..03、TC-07-04
- 前置：T1.1
- 改动面：`backend/internal/pkg/sshpool/`、`backend/internal/service/{ssh,terminal}.go`、`backend/internal/service/`（gate/registry）、遥测注册表、`backend/internal/handler/metrics.go`（图表契约）
- 步骤：
  1. 落 lease 接口：`Acquire/Release/Discard` 带 generation；`Lease{Exec,Upload,Cancel,Done,Release,Discard}`；错误分类枚举。
  2. 落 `UserGate` 与 `TerminalRegistry` 接口（preparing/ready/revoked + done）。
  3. 落遥测注册表：日志 schema_version=1 枚举、指标名/标签固定枚举（含 lease Gauge owner 唯一）。
  4. 落图表查询响应契约（`range`/`interval`/`actual_interval`/`coarsen_reason`/`stat`/`coverage`/`source`/`downsampled`/`null`）与 mock；字段名与需求 §4.2.1 一致，前端 decoder 同步迁移。
  5. 只加定义与测试骨架，不改运行行为；记录 p95/p99 基准（§5.3 条件）。
- DoD：四类契约有接口、枚举、mock 与编译；基准测量报告产出（值可超目标但必须记录）。
- 回滚：接口文件可 revert；不触及调用点。

#### T1.6 迁移框架硬化（首个新迁移的前置）
- REQ：REQ-08（迁移正确性）；TC：TC-08-06（幂等部分）
- 前置：T1.1
- 改动面：`backend/internal/repository/migrations.go`（owner：repository 层）
- 步骤：
  1. migration ID 锁 + 原子事务：锁→检查→SQL→marker 同一事务提交；中断/失败不留部分 marker。
  2. 失败可安全重跑（幂等）；并发/重复执行不双重应用。
  3. 中断、重跑、并发竞争三类用例。
- DoD：迁移硬化用例通过；`users.token_version`（T2.5）与 rollup 建表（T3.4）之前必须完成。
- 回滚：保留旧 marker 语义（仅限故障隔离）。

### Phase 2 可靠性与会话

#### T2.1 SSH lease 资源层（P0，关键路径）
- REQ：REQ-06；TC：TC-06-01、TC-06-02、TC-06-03
- 前置：T1.5
- 改动面：`sshpool/pool.go`、`service/ssh.go`（`Exec`/`CopyFile` → lease）、调用点 `handler/exec.go`、`service/monitor.go`、`service/terminal.go`
- 步骤：
  1. `pool` 条目加 `generation`；`Release`/`Discard` 校验 generation；不符者降级 Discard + `reason=stale_generation` 日志。
  2. `ssh.go` 以 `AcquireLease` 重写 `GetClient` 路径；`Exec`/`CopyFile` 基于同一 lease 且不嵌套借用。
  3. 上传路径：传入绝对 `LOCAL_CLEANUP_BUDGET=3s`，grace 100ms，贯通父 context 取消，显式 join 上传 worker。
  4. lease Gauge：资源层唯一维护 `consumer=agent`（占用 +1 / 归还丢弃幂等 -1）。
  5. 测试：SSH 模拟对端 + 固定并发时序 + `-race`；覆盖成功、源文件失败、session 失败、传输中断、阻塞对端、父取消、重复上传后配额可用。
- DoD：TC-06-01..03 通过；连续超配额后 Exec/Terminal/采集仍可用；Gauge 回基线；无泄漏/死锁。
- 回滚：由于签名变更一次性替换调用点，revert 需整体回退本任务。

#### T2.2 Exec 超时与输出限额
- REQ：REQ-07；TC：TC-07-01、TC-07-02
- 前置：T2.4（写 deadline 先就绪）
- 改动面：`config/config.go`（新增配置 + `Validate`）、`handler/exec.go`、`service/ssh.go`（分阶段预算）
- 步骤：
  1. 移除 Handler 硬编码 30/300，改为 `请求值 → EXEC_TIMEOUT(默认30) → 上限300`。
  2. 阶段预算：quota_wait/dial/run/cleanup 各取剩余；清理用一次性绝对截止，grace 100ms。
  3. 输出限额：`EXEC_OUTPUT_LIMIT=8MiB`（总）、`EXEC_OUTPUT_STREAM_LIMIT=4MiB`（单流）；达限后有界读取丢弃、不取消命令；仅实际丢弃时置 `*_truncated`；返回 retained/discarded 字节与真实退出码。
  4. `Config.Validate`：拒绝非正数、单流>总量、非法 timeout；`MONITOR_INTERVAL>0`。
- DoD：TC-07-01/02 通过；20–30s Exec 完整返回；恰好到达上限不算截断；内存不随产量无限增长。
- 回滚：回退代码即可；限额是冻结契约（需求 §7.1.2），环境变量只能降低、**不得关闭或超过**。

#### T2.3 Relay 模式与受限长流
- REQ：REQ-07；TC：TC-07-03
- 前置：T2.4（写 deadline 先就绪）
- 改动面：`service/service_relay.go`、`handler/service.go`、`cmd/server/main.go`（路由级写 deadline）
- 步骤：
  1. 请求 `mode=standard|bounded_stream`（省略=standard，非法 400，不转发上游）。
  2. standard 总 30s；bounded_stream 总 300s；header 等待与 body/写无进展均 30s，且不超剩余总预算；idle 心跳不重置总截止。
  3. 响应头未发可 504；已发则中止传输，记录上游状态/已复制字节/失败阶段，不追加第二 JSON。
  4. 长路由用 `ResponseController.SetWriteDeadline`（每次写前 re-arm），不被 15s 全局写超时或 30s client timeout 截断；禁止无限 SSE/101。
- DoD：TC-07-03 通过；普通 CRUD 保护不丢。
- 回滚：mode 字段可选，关闭 bounded_stream 即退回 standard。

#### T2.4 HTTP 超时与写保护收口
- REQ：REQ-07；TC：TC-07-01（长路由）
- 前置：T2.1（先于 T2.2/T2.3）
- 改动面：`cmd/server/main.go`（WriteTimeout 路由化）、部署文档（反向代理 timeout）
- 步骤：
  1. 全局 `WriteTimeout` 不再一刀切 15s；长 Exec/Relay 路由适配，普通 CRUD 保持保护。
  2. 反代/网关 timeout 必须覆盖**完整请求预算 + 结果返回与清理余量**（Exec 300s、bounded_stream 300s 之外再留余量），不能写死 300s，否则长操作在代理层被截断；写入部署说明。
- DoD（本任务）：路由写 deadline 生效——普通 CRUD 仍受保护，长路由不再被 15s 提前截断（TC-07-01 的 deadline 部分）；**长 Exec、受限 Relay 的完整传输验收在 T2.2/T2.3 实现后联合完成，本任务不单独宣称。**
- 回滚：恢复全局值。

#### T2.5 JWT 撤销：schema / HTTP / WS
- REQ：REQ-05；TC：TC-05-01、TC-05-03、TC-05-05
- 前置：T1.5、T1.6（迁移硬化先于本任务）
- 改动面：迁移（`users.token_version`）、`pkg/token/jwt.go`、`server/middleware/auth.go`、`service/auth.go`（ChangePassword）、**新增共享 `UserGate`（HTTP 准入 + 未决状态 + 有界核实 + 重启恢复）**、前端 `lib/auth.ts`
- 步骤：
  1. 迁移加 `token_version bigint not null default 0`（经 ApplyOnce，非 AutoMigrate）。
  2. JWT 加 claim；每次 HTTP JWT 与 WS 首帧查主库版本（禁正向缓存）；缺 claim 的历史 JWT 失效。
  3. 新增按 UserID 共享的 `UserGate`：HTTP JWT 准入与密码更新共用；COMMIT 为线性化点。
  4. `ChangePassword` 事务内锁行、校验原密码、递增版本；COMMIT 报错且结果未知时在 gate 下标记未决、取消 JWT 会话并返回可区分的 503；用**有界**主库行锁/事务状态核实（单次读旧版本不算回滚），确认后解除未决，超时不无限持 gate。
  5. 重启恢复：Hub 启动后该用户 gate 首次准入通过有界主库加锁读取排除上次未决密码事务，不靠重启放行。
  6. 前端 401 清理认证与敏感缓存、跨标签同步、回登录页。
  7. 测试：固定时序 + 未决提交、重启恢复、清理超预算分别断言。
- DoD：TC-05-01/03/05 通过（含未决 COMMIT 拒绝准入、有界核实与重启恢复）；旧 JWT 401，新 token 可用；API Key 不受影响；**共享 `UserGate` 就绪，供 T2.6 复用**。
- 回滚：保留列，回滚代码；**回滚后的构建仍须保留版本校验与主动撤销**（否则已撤销 JWT 会复活），禁止回退到无版本校验的版本。

#### T2.6 终端 registry 与 3s 清理
- REQ：REQ-05/06/07；TC：TC-05-02、TC-05-04、TC-06-03、TC-07-04
- 前置：T2.5（token_version）、T2.1、T2.2（lease + 预算）
- 改动面：`service/terminal.go`、`handler/terminal.go`、`service/`（**复用 T2.5 的 `UserGate`**，新增 `TerminalRegistry`）
- 步骤：
  1. 首帧初次校验 → gate 内二次查主库 → 登记 preparing → 再等配额/拨号。
  2. ready 前 gate 内复查版本与会话状态；已撤销的晚到连接不得获得 ready/输入许可。
  3. COMMIT 后释放 gate 前标记 revoked + cancel；gate 外关闭并 join，总截止 3s（终端 grace 2s）。
  4. 幂等 Done；只回收该会话资源，不关整池。
  5. 固定时序测试：暂停在初次校验后/登记后/ready 前并发改密码。
- DoD：TC-05-02/04、TC-06-03、TC-07-04 通过；阻塞对端验证 3s 总预算；无残留 WS/handler。
- 回滚：保留 `token_version` 与版本校验/主动撤销（禁止回退到无校验构建）；**不得把 registry 降级为纯登记绕过撤销**。

### Phase 3 采集与图表

#### T3.1 Monitor 生命周期与调度
- REQ：REQ-08；TC：TC-08-01、TC-08-07
- 前置：T1.5（遥测契约）
- 改动面：`cmd/server/main.go`（根 context + 退出 join）、`service/monitor.go`、`service/server.go`（statusThreshold）、`config/config.go`
- 步骤：
  1. Monitor 接入 App 根 context；退出停止调度、取消在途、等待回收，再关连接/存储。
  2. 调度数值（需求 §8.1.1）：min(N,16) 在途、每节点 ≤1 在途 + 1 待调度、准入后 30s 总 deadline、失败退避 base=min(I×2^(k−1),max(I,300s)) ±10% jitter clamp 到 [I,cap]，成功清零、不补跑；退出/删除/generation 取消不累计失败。
  3. `statusThreshold` 由 I、容错轮数与允许延迟推导，去掉固定 120s。
  4. 遥测：`monitor.collect` action，记录成功/失败/超时/漏采/延迟/排队；告警按需求 §12.3。
- DoD：TC-08-01 断言零抖动 I=60s → 60/120/240/300/300s；17 节点最多 16 在途；退出后无残留 worker。
- 回滚：恢复全局 ticker（不推荐，回滚仅限故障隔离）。

#### T3.2 Agent 产物、协议输出与 Hub 镜像 fail-fast
- REQ：REQ-08；TC：TC-08-05、TC-08-08（启动 fail-fast + Agent 协议输出）
- 前置：T1.5
- 改动面：新增 `backend/build/agents.sh`（含版本/协议注入与 manifest）、`cmd/agent`（输出 `agent_version`/`protocol_version`）；`Dockerfile`、`backend/Dockerfile`；`cmd/server/main.go`（启动校验）；CI
- 步骤：
  1. `agents.sh` 交叉编译 linux/amd64 与 arm64（`CGO_ENABLED=0 GOOS=linux`），**以 ldflags 注入构建版本与协议版本**，生成含 platform/build version/protocol version/SHA-256/size 的 manifest；manifest 与运行输出共用同一协议常量。
  2. `cmd/agent --format json` 输出顶层 `agent_version`（非空构建标识）与整数 `protocol_version`（首版=1）；两架构构建注入相同版本。
  3. 两个 Dockerfile 复用该入口、仓库根 build context；两种 Hub 镜像均带两份 Agent + manifest。
  4. 启动时（绑定 HTTP 端口前）校验清单与产物；缺失/损坏/架构·size·hash·协议不匹配 → fail-fast 非零退出，不启 HTTP/Monitor；不隐式单架构降级。
  5. CI 验证两镜像内部 ELF 架构、size/hash，并做**协议 JSON 烟测（本任务自包含，不依赖 T3.3）**：断言输出含匹配的 `agent_version` 与 `protocol_version=1`。
- DoD：TC-08-05、TC-08-08（启动 fail-fast 与 Agent 协议输出）通过；本地产物错误时启动非零且无监听；两架构输出协议字段一致。
- 回滚：**无隐式单架构降级**；发布故障时回退到上一个完整双产物镜像，而不是缺产物的构建。

#### T3.3 AgentDeploymentService：探测 / 部署 / 协议校验（Hub 侧）
- REQ：REQ-08；TC：TC-08-02、TC-08-04、TC-08-08（Scenario B）
- 前置：T2.1（lease）、T3.2（产物 / manifest / 协议输出）
- 改动面：新增 `service/agent_deploy.go`（AgentDeploymentService）；`service/monitor.go`（`collectOne` 部署逻辑迁出）
- 步骤：
  1. 探测：一条复合命令取 `uname -s`/`uname -m`（5s 预算，受总 deadline）；映射 x86_64→amd64、aarch64→arm64；不支持平台/架构拒绝，失败上传为 0。
  2. 远端检查用主机自带工具读存在状态与 SHA-256；临时文件同文件系统、上传→校验→chmod→原子 rename；`/tmp` noexec/只读/缺工具分列归类。
  3. 同目标/generation 一份 lease 贯穿探测/上传/执行；激活前本地短临界区确认 generation，失效不再准入。
  4. Hub 侧协议校验：仅接受 `protocol_version=1`（精确）且 `agent_version` 匹配 manifest 的输出（字段由 T3.2 产出）；缺字段按 legacy 拒绝入库、不写部分指标；激活结果未知经实际 hash 核实。
  5. 唯一 owner：Monitor 只调度/校验/入库，不再内联上传。
- DoD：TC-08-02/04、TC-08-08-B 通过；远端失败不退出 Hub；历史指标保留。
- 回滚：回退代码；**不得保留“旧上传路径”开关绕过 lease/协议校验**。

#### T3.4 指标差分、6h rollup 与保留
- REQ：REQ-08；TC：TC-08-03、TC-08-06
- 前置：T3.3
- 改动面：`repository/metric.go`、新增 `metrics_rollup_6h` 与检查点迁移、新增聚合/清理 job
- 步骤：
  1. 相邻样本 `delta/time`：计数器 reset、零/负时间差、超断采阈值区间不参与，返回未知并成缺口；真实零增量=零；单独保留已采样区间峰值。
  2. 自维护 `metrics_rollup_6h`（桶以 `(server_id,bucket_start)` 唯一定位）：桶结果与完成检查点同事务提交、重算幂等；upsert + advisory lock 防并发；检查点覆盖已确认空桶。
  3. raw/rollup 均保留 35d；每小时重算最近 48h（首部署回填 35d）；每天按完整 chunk 清理，删前确认聚合完成，失败暂停并告警。
  4. 查询拼接：完整已物化桶走 rollup，两端/未闭合/未物化桶按边界从 raw 算，`[start,end)` 互斥；其他粒度从 raw 读取受 §4.2.1 预算约束。
  5. 容量实测：记录 1d/7d 表/索引/chunk 大小并外推。
- DoD：TC-08-03/06 通过；30d 结果与 raw 的平均/max/coverage 对照正确；迁移幂等。
- 回滚：停调度并保留结构；已删 raw 不可逆，故 dry-run + 删前确认是硬前置。

#### T3.5 图表查询预算与后端契约
- REQ：REQ-04/08；TC：TC-04-02、TC-04-05
- 前置：T3.4
- 改动面：`handler/metrics.go`、`repository/metric.go`
- 步骤：
  1. 输出上限 600 桶（按 UTC 边界含空桶与两端部分桶）；合法粒度 1m/5m/15m/1h/6h。
  2. `auto` 自动升粗；API 显式粒度默认 strict，仅 `allow_coarsen=true` 允许改变；strict 超限 422 `metrics_query_budget_exceeded` 并附推荐粒度；非法粒度/超 30d 返回 400，不缩短 from/to。
  3. 计划 raw 输入 ≤12,000（探针读 ≤12,001 候选键，不无界 COUNT）；最多两个计划，探针/读取/聚合共用 2s deadline。
  4. 转数据源才减少 raw 输入（不扩 bucket 仍全扫 raw）；rollup 未就绪且回退超预算 503 `metrics_rollup_not_ready`；deadline 到期 503。
  5. 响应含 requested/actual interval、stat、coverage、source、downsampled。
- DoD：TC-04-02/05 通过；600/601 桶、12,000/12,001 样本边界与 422/503 语义正确。
- 回滚：关闭强制降级，退为显式报错。

#### T3.6 图表 UI 与可访问性
- REQ：REQ-04；TC：TC-04-01、TC-04-03、TC-04-04、TC-04-05（UI 部分）
- 前置：T1.5（mock 契约），真实数据接 T3.5
- 改动面：`types/metrics.ts`、`features/monitoring/components/{time-range-selector,trend-chart}.tsx`、`lib/series.ts`、`hooks/use-metrics.ts`、新增数据替代视图
- 步骤：
  1. 时间范围加 15m/30d；纵轴分百分比/吞吐/Load；平均/峰值切换；页面共享范围与统计方式。
  2. 缺值断线、真实 0 显示 0、无 NaN/Infinity；切换取消旧请求/隔离迟到；后台页停周期请求。
  3. 可访问性（需求 §4.5）：线型/marker 非纯颜色辨识、对比度、焦点、读屏、语义 table 数据视图。
  4. `downsampled=true` 的可见提示。
- DoD：TC-04-01/03/04 通过（读屏人工项单独记录）；375px/键盘可用。
- 回滚：保留旧图表组件开关。

### Phase 4 终端体验

#### T4.1 xterm UI 功能
- REQ：REQ-09；TC：TC-09-01
- 前置：T2.6
- 改动面：`frontend/src/features/terminal/*`
- 步骤：顶栏信息/状态/重试计数；手动连接/断开/重连；全屏与布局；字号与主题本地记忆；xterm 官方搜索；剪贴板/多行粘贴/清屏（仅客户端）；fit + 防抖 PTY 尺寸。
- DoD：TC-09-01 通过；token 不放 URL。
- 回滚：组件级 revert。

#### T4.2 生命周期与移动端
- REQ：REQ-09；TC：TC-09-02
- 前置：T4.1
- 改动面：`features/terminal/hooks/use-terminal.ts` 及布局
- 步骤：切主机/连续重连/卸载/密码撤销后无重复 WS/handler/timer；断连回收配额；375px 与横屏、控件 ≥44px。
- DoD：TC-09-02 通过；撤销后无残留会话。
- 回滚：保留旧连接逻辑开关。

---

## 5. 测试、CI 与可观测性计划

- 测试分层：`fast`（逻辑 + 静态断言，含适配器检查）/ `integration`（隔离 DB、迁移、镜像产物）/ `ui`（mock + 人工读屏）/ `e2e`（真前端+Hub+受控 SSH）；`all` 为并集，缺必需检查非零。
- 用例登记：所有实现写入 `tests/coverage.json`；`automation_status` 只描述实现状态（planned/implemented/manual）——**已实现但未运行仍是 `implemented`**；“未运行/缺证据”是**执行结果 pending**，写在 run/execution 记录里，不写回 automation_status，也不伪装通过；`coverage.md` 由 JSON 生成且 CI 校验无 drift。
- 并发与资源：lease/上传/撤销/清理类用例用固定时序 + 可控时钟/RNG，并执行 `-race`。
- 遥测验收（需求 §12）：统一断言有效 deadline、失败分类、最终事件恰好一次、Counter 一次、Gauge 回基线、日志无敏感字段、标签有界。
- CI 新增：`tests/run.sh --suite fast`、`render-coverage.mjs --check`、双 Agent 镜像内部校验。
- 人工项：仅**读屏记录**（NVDA+Firefox、VoiceOver+Safari，含版本）为人工；**真实 E2E 属自动化**（`e2e` suite），不得整体归入人工项。

## 6. 迁移与发布顺序

1. 先完成 **T1.6 迁移框架硬化**（migration ID 锁 + 事务）；再加列/建表（向后兼容）：`users.token_version`、`metrics_rollup_6h` + 检查点，均走硬化后的 `ApplyOnce`，不用 `AutoMigrate`。
2. 发代码：先 lease/预算（T2.1–T2.4），再撤销（T2.5–T2.6），再采集/图表。
3. 最后开调度：rollup/retention 默认关 → dry-run → 开；Agent 双产物随镜像发布。
4. **首次启用撤销机制：不得滚动共存**——先停止旧 Hub 并结束旧 WS 会话，再启动带版本校验的新构建（coverage 登记为一次性切换，不接受“窗口内旧副本放行”）。后续发布与回滚均保留版本校验与主动撤销，不再存在允许旧副本并行服务的窗口。
5. 唯一不可逆操作是 raw 清理，必须先确认聚合完成。
6. 隔离 TimescaleDB 验证新库与历史升级路径，不以禁用外键的仓储测试替代。

## 7. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| lease 签名变更影响面广 | T2.1 一次性替换全部调用点（仅 Exec/CopyFile/Terminal 三处）+ 全量 race |
| 每请求主库版本查询延迟 | T1.5 先测基准；仅 2s 故障截止；不以缓存换达标 |
| 查询保护实测偏差 | T3.5 用 600/601、12,000/12,001 fixture 冻结规则 + EXPLAIN |
| rollup/retention 误删 | 删前确认聚合完成；失败暂停删除；dry-run 先行 |
| fail-fast 阻断启动 | CI 保证两产物齐全；发布前镜像内校验 |
| 静态检查误报 | 豁免逐文件登记；新增规划文档同步登记；不用通配豁免 |

## 8. 交付完成定义（DoD）

- 每个任务：业务行为 + 对应 TC 实现并登记 + 文档/升级说明；CI 相关门禁通过。
- 阶段门禁：M1–M4 全部满足对应 TC 集合，缺一不算完成。
- 全局：UI 与 API 描述一致；无候选数值开工；不可逆操作有 dry-run 与回滚说明。
- 文档：需求/架构/技术方案/开发方案四份相互链接可解析，新增文档同步 `scan_exemptions`。
