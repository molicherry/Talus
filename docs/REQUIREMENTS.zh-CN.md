# Talus 改进需求与验收

> 整理日期：2026-10-09；当前代码基线：`21d53b4`。
> 本文记录用户已确认的目标、建议实现边界与验收用例，配套 [架构文档](ARCHITECTURE.zh-CN.md)。本轮只更新文档，未实施功能、迁移测试文件或删除适配器；下列需求均待实施。

## 1. 范围与已确认决策

| ID | 需求 | 已确认方向 | 优先级 |
| --- | --- | --- | --- |
| REQ-01 | 移除平台适配器 | 删除 ai-integration 及专属说明；保留 skills/talus、API 和服务能力 | P2 |
| REQ-02 | 统一测试用例管理 | 集中英文用例说明、统一模板/索引/执行入口；保留 Go 包内测试能力 | P1 |
| REQ-04 | 图表显示尺度与交互 | 参考 Beszel；区分时间范围、点分辨率、纵轴与统计方式 | P1 |
| REQ-05 | 改密码撤销会话 | 旧 JWT 失效、旧 JWT 终端回收，API Key 独立管理 | P1 |
| REQ-06 | Agent 上传资源与取消修复 | 配额/client/session/worker 生命周期完整，无泄漏 | P0 |
| REQ-07 | 执行与传输超时优化 | 统一预算，长 Exec/Relay 不被 15 秒 HTTP 写超时提前截断 | P1 |
| REQ-08 | 监控、Agent 与指标优化 | 独立调度、退出回收、采集程序的上传/更新和正确差分 | P1 |
| REQ-09 | xterm 终端 UI 优化 | 保留内核与 SSH/WS 链路，完善可操作性和显示 | P2 |

P0 表示优先修复可累积的资源泄漏或阻塞；P1 表示近期正确性与核心能力；P2 表示产品清理或体验。优先级不等于工作量，适配器清理可以独立并行推进。

仍保留模块化单体、React、Go、PostgreSQL/TimescaleDB、SSH 和 xterm。Beszel 用作图表交互参考，不迁移其 Agent、PocketBase 或完整前端。多标签/分屏、微服务拆分、完整多用户 RBAC、服务 SSH 隧道和零丢失审计不纳入首期。

原 REQ-03 已按用户要求取消，编号不复用。监控显示按 REQ-04 推进，基于现有采集数据改善时间范围、点分辨率和纵轴；本次不新增页面采集频率设置、采集设置持久化/热更新、常规秒级采集或 1 秒实时模式及其评估任务。

## 2. REQ-01：移除适配器，保留 skill

### 2.1 实施范围

- 删除 `ai-integration/` 中 pi、OpenCode、Claude Code、Codex 的适配器、安装器和专属 README。
- 中英文 README 移除“安装服务目录注入插件”、适配器下载命令、自动目录注入和 hook 缓存说明；保留 skill 安装、环境变量、API 和 Relay 的使用方法。
- 保留 `skills/talus/SKILL.md` 文件及核心操作流程，仅清理其中指向已移除适配器/安装器的说明。
- 保留 `TALUS_URL/TALUS_API_KEY` 使用约定、API Key、服务目录、`usage_guide`、服务 Relay 和相关 UI。
- 同步更新目录树、架构说明及所有适配器引用。不新增另一组平台适配器。

### 2.2 验收

1. 仓库内不再有适配器代码、安装器或有效安装说明；需求/变更记录中的历史提及不算残留安装入口。
2. skill 文件存在，独立安装方法可读；没有失效的适配器链接。
3. skill 所需 API 契约和 scope 没有因清理而删除，服务指南及 Relay 功能保持可用。

### 2.3 可执行静态检查

拟新增 `tests/static/adapters-removed.sh`，实现 TC-01-01，并由 `bash tests/run.sh --suite fast` 和 `all` 必须执行。当前只定义该脚本的行为，尚未创建脚本或删除适配器。

- 从仓库根目录执行；断言 `ai-integration/` 不存在，空目录和悬空符号链接也失败。按已知产物清单检查旧安装器/适配器路径，包括 `inject-service-skills.js`、`inject-service-skills.py`、`service-skills/index.ts`。
- 用 `rg --files --hidden --no-ignore` 建立扫描清单，覆盖源码、README、skill、隐藏 CI/配置和测试。内容规则查找 `ai-integration/` 的安装/下载引用、上述安装器名、`<service-skills-directory>` 和旧“安装服务目录注入插件”等专属说明；不把 Codex、OpenCode、skill、plugin、hook 等泛词列为禁词。
- 只排除 `.git/`、明确列出的依赖/构建缓存，以及登记的历史/规划文档、检查器和专用负例的规则文字。内容豁免以未来 `tests/coverage.json` 的 `scan_exemptions` 为唯一清单，初始登记 `docs/ARCHITECTURE.zh-CN.md`、`docs/REQUIREMENTS.zh-CN.md`、`docs/IMPLEMENTATION.zh-CN.md`、`docs/DEVELOPMENT-PLAN.zh-CN.md`（四份规划文档随仓库提交，故干净 CI 也能读到）与 `tests/static/adapters-removed.sh`，专用负例 `tests/fixtures/adapters-removal/` 允许目录前缀；每项记录 path、kind 与英文 reason。历史/规划/检查器必须为精确文件路径；新规划文档与其豁免登记随同一变更审阅，不能用 `docs/**`、`tests/**` 或任意注释豁免。目录/产物不存在的断言不受内容豁免影响。
- 正向断言 `skills/talus/SKILL.md` 非空；两份 README 的 skill 安装链接有效、独立安装步骤及 `TALUS_URL/TALUS_API_KEY` 约定保留；skill 的 `X-API-Key`、`services:relay`、`usage_guide` 和服务列表/详情/Relay API 说明保留。API 可用性另由 TC-01-02 验证，文本存在不能代替接口测试。
- 禁词扫描的 `rg` 返回码 0 表示发现残留、1 表示无匹配、2 或其他表示扫描错误；正向匹配必须为 0。检查器总返回码为 0=全部通过、1=断言失败、2=工具/扫描错误；缺少 `rg`、必需文件或读取失败均失败，不以 `|| true` 吞错。
- TC-01-03 在临时目录注入源码残留、空目录/软链接、旧安装 URL、缺失 skill 和扫描错误，验证检查器拒绝；合法 skill 安装与白名单历史记录通过。

## 3. REQ-02：统一测试组织与格式

### 3.1 统一到什么程度

前后端测试代码使用各自语言的原生语法，统一用例名称、行为说明、前置条件、输入/操作、预期、分类、环境和结果格式。根目录 `tests/` 集中全部用例说明，并能找到每个用例的自动化实现；统一入口调度实际运行器。

用例说明使用英文：`tests/cases/` 内的标题、字段名、Scenario、Given/When/Then、环境说明与预期结果，以及覆盖索引中的用例描述均用英文。需求与架构的主体说明继续使用中文；用例 ID 和需求 ID 保持稳定。

Go 同包 `*_test.go` 可访问未导出的实现，目录决定 package。全部搬到根目录会改变包身份和 module/internal 导入边界，不能以形式统一为代价丢失有效覆盖。现有前端与 Go 测试实现先保留；新增跨端测试和共享数据集中管理。后端专用 `testdata` 与前端独有 fixture 可保持在所属模块。

### 3.2 目标目录

```text
# 拟新增，当前尚未创建
tests/
├── README.md                 分类、运行命令、环境和 CI 规则
├── cases/
│   ├── auth.md               Authentication and revocation cases
│   ├── ssh.md                Quota, upload, execution, and timeout cases
│   ├── monitoring.md         Scheduling, Agent deployment, and metrics cases
│   ├── charts.md             Chart scales, statistics, and interaction cases
│   ├── terminal.md           xterm and connection lifecycle cases
│   └── integration.md        Cleanup, startup, upgrade, and cross-component cases
├── coverage.json             Sole machine-readable case and run manifest
├── coverage.md               Generated human-readable coverage report
├── render-coverage.mjs       Deterministic report generator and drift check
├── fixtures/                 公共合成数据，不存真实凭据
├── e2e/                      真实前端 + Hub 的流程测试
├── integration/              部署、TimescaleDB 启动/升级验证
├── static/                   Repository and documentation assertions
└── run.sh                    统一执行入口
backend/internal/**/*_test.go  保留 Go 原生实现
frontend/tests/               保留现有 Node / Playwright 实现
```

用例说明统一模板（供人阅读，执行元数据只维护在 coverage.json）：

```text
Case ID: TC-06-01
Requirement: REQ-06
Scenario: SSH capacity remains available after repeated Agent uploads.
Level: Go unit / SSH protocol simulation
Given: A controlled SSH peer with a known concurrent lease limit.
When: Complete more uploads than the lease limit, then execute a command.
Then: All upload leases are released exactly once, execution succeeds,
      and all upload workers have exited within the total local teardown budget.
Environment: An isolated SSH test peer; no production server is required.
Execution metadata: Resolve this Case ID in tests/coverage.json.
Coverage report: Read the generated tests/coverage.md.
```

#### 3.2.1 执行元数据唯一来源

未来 `tests/coverage.json` 是唯一机器源，使用 JSON 标准解析并校验 schema，不从 Markdown 自由文本或 fenced block 推断执行计划。`tests/coverage.md` 由拟新增的 `tests/render-coverage.mjs` 确定性生成，供人阅读、禁止手改或反向解析；主体/§10 仍是场景摘要。`node tests/render-coverage.mjs --check` 校验生成报告与 JSON 一致，差异返回非零，CI 不允许两份元数据分叉。机器清单、生成器与报告当前均尚未创建。

| JSON 结构 | 必需字段与约束 |
| --- | --- |
| 顶层 | `schema_version=1`、`runs`、`cases`、`scan_exemptions` |
| runs[] | 唯一 `run_id`、`suite`、`environment`（profile/requires）、`runner`、`cwd`、`command`（argv 数组）、`timeout_seconds`；manual 的 command=null、timeout_seconds 可为 null，另有操作规范与证据要求 |
| cases[] | 唯一 `case_id`、`requirement_ids`、`spec_ref`、英文 scenario、implementations[] |
| implementations[] | 唯一 `implementation_id`、`run_id`、`implementation`（path/symbol）、`automation_status`（planned/implemented/manual）、`required` |
| scan_exemptions[] | path、kind（planning/history/checker/negative_fixture）、英文 reason；遵循 §2.3 的精确路径约束 |

Suite、Environment、Command 通过 run_id 关联，Implementation、Automation status 在实现记录中；一次 Case 可有多个实现，例如浏览器自动化和人工读屏分别登记。已实现项必须给出实际路径/测试符号与合法 argv；planned 的 implementation/command 可为 null，但选中且必需时标记 pending 并返回非零，不能伪装通过。

`suite` 仅允许 fast/integration/ui/e2e，`all` 是四者并集，不写入元数据。runner 固定为 go-test/node-test/playwright/shell/manual；在声明 cwd 中按 argv 执行，不 eval 自由 shell 字符串。同一 run_id 只执行一次，其结果关联多个 Case；若 runner 不报告逐 Case 结果，只标注 runner 级覆盖，不据其退出码虚构所有 Case 已通过。manual 实现必须引用 manual runner，不执行伪命令，声明操作规范/证据，缺少必需证据保持 pending；运行结果以 `execution_id` 标识并另含 commit、时间、环境与证据，写入 `tests/.artifacts/exec-<execution_id>.json`；`run_id` 始终是静态运行定义，不反写用例定义为“已通过”。

§10 的 Test level 仅供人理解验证方式；Go/race/Telemetry/Build 不决定 suite。真实依赖决定 suite：无 DB/浏览器/真实 Hub 的模拟归 fast；隔离 DB、迁移和 Docker 产物验证归 integration；mock API 浏览器及人工读屏归 ui；真实前端/Hub 流程归 e2e。机器清单必须显式指定，不能根据语言、后缀或摘要猜测。

### 3.3 执行分类

| 分类 | 执行范围 | 环境与结果规则 |
| --- | --- | --- |
| fast | 不依赖数据库/远程主机的 Go、前端逻辑及仓库静态断言 | 必须执行适配器移除检查；不把未跑集成测试计为通过 |
| integration | 仓储、TimescaleDB 启动/升级、跨组件协议和镜像产物验证 | 隔离数据库或 Docker/QEMU 等显式依赖；选中分类但缺少依赖时失败 |
| ui | 生产构建上的 mock API 浏览器回归 | 显式标为 UI 回归，不称为真实 E2E |
| e2e | 实际前端 + Hub + 隔离数据库，SSH 用可控测试对端 | 覆盖真实认证、图表数据查询和终端链路 |
| all | 汇总上述分类及现有必需检查 | 输出通过/失败/跳过，失败或必需检查缺失返回非零 |

拟定统一命令为 `bash tests/run.sh --suite <分类>`，具体 runner 保留 Go testing、Node/Playwright。不会为了统一说明而立即引入另一套测试依赖。

### 3.4 验收

1. 全部已有用例可在中央目录/索引查到，明确实际覆盖、运行命令与自动化状态；不是只有目录名而没有用例。
2. 从仓库根目录运行统一入口能够定位正确工作目录；各子运行器失败会向上传递。
3. Go 包内覆盖、现有前端回归和 CI 检查不丢失；明确 mock UI 与真实 E2E 的差别。
4. 所有新需求关联用例 ID，缺失必需环境不会被静默标成通过；测试不会默认连接真实服务器或生产库。
5. 中央用例说明与描述统一为英文，遵循同一字段和 Given/When/Then 模板。
6. coverage.json 在任何 runner 执行前校验 schema、唯一 ID、Case/run/implementation 引用、suite/状态/命令及环境；覆盖实现完整登记，§10 不承担运行器输入职责。生成的 coverage.md 差异检查失败必须返回非零。Phase 1 交付须包含两份落盘设计文档和机器清单定义，未来 runner/清单尚未创建的状态不能写成已可执行。

## 4. REQ-04：图表尺度与 Beszel 参考

### 4.1 参考边界

参考 Beszel 的范围选择、图表卡片、历史平均/峰值、单位与 tooltip。使用 Talus 现有 React 与图表边界实现，不要求复制 Beszel 的后端或 UI 源码。下表的实时能力仅说明参考项目的设计，不纳入 Talus 本次需求。

核对来源为官方仓库快照 `bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3`：

| 已核实的 Beszel 设计 | 官方来源 |
| --- | --- |
| 1m 实时及 1h/12h/24h/1w/30d 范围；历史粒度随窗口变化 | [窗口与粒度配置](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/lib/utils.ts#L128) |
| 长窗口可选择 Average/Max | [图表卡片](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/routes/system/chart-card.tsx#L67) |
| 短时窗口采用实时订阅，Hub 有订阅时提供秒级数据 | [实时数据 hook](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/routes/system/use-system-data.ts#L143)、[Hub 实时循环](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/hub/systems/system_realtime.go#L141) |
| 通用 Y 轴默认自动范围，内存图结合总容量；不是所有图都固定 0–100% | [通用轴](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/charts/area-chart.tsx#L145)、[内存图](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/routes/system/charts/memory-charts.tsx#L27) |
| 时间、系列、单位提示；长采样缺口插入 null | [Tooltip](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/ui/chart.tsx#L94)、[缺口处理](https://github.com/henrygd/beszel/blob/bc2278e7e9820825ce990fb2fe7fca4b9fe9a1c3/internal/site/src/components/routes/system/chart-data.ts#L26) |

以下范围、默认轴和 bucket 是 **Talus 的设计提案**，不称为 Beszel 原样功能。

### 4.2 显示尺度

| 维度 | 首期要求 |
| --- | --- |
| 横轴时间范围 | 保留 1h/6h/24h/7d，新增 15m/30d；默认 1h |
| 点分辨率 | 默认自动选择，页面显示实际粒度；提供适合当前时间范围的 1m/5m/15m/1h/6h 聚合粒度，不能宣称具有细于实际采样的分辨率 |
| 百分比纵轴 | CPU/内存/磁盘/Swap 默认 0–100%；可切换自适应观察小幅变化，自适应时明确标识 |
| 吞吐纵轴 | 从 0 到自适应上限，RX/TX 或 read/write 共用刻度；B/s、KB/s、MB/s、GB/s 使用一致十进制换算 |
| Load 纵轴 | 独立无单位数值轴，不与百分比/吞吐共用量程 |
| 历史统计 | 平均/峰值切换，默认平均；同一服务器页面共用时间窗口和统计方式 |

默认一分钟采集时，历史自动 bucket 的初始建议：

| 时间范围 | 自动 bucket 初始值 |
| --- | --- |
| 15m / 1h | 1m |
| 6h | 5m |
| 24h | 15m |
| 7d | 1h |
| 30d | 6h |

保持现有采集机制，默认采集周期 60 秒，当前查询最小 bucket 为 1 分钟。图表粒度改变聚合和显示方式，不改变采集周期；历史数据不足或间隔较大时标明实际粒度和采样间隔，不能插值伪造秒级样本。首期每图最多 600 个桶（含空桶和部分桶），查询预算及降粒度规则见下一节；响应返回实际粒度/统计方式，防止前后端独立猜测。30d 范围受 metrics 保留策略约束，不足时显示真实可用范围。

峰值来自桶内实际采样的 max，长期降采样也要保留这一信息；不能用均值曲线的最高点冒充原始采样峰值。未采到的瞬时峰值无法恢复，界面应以“已采集样本的峰值”理解。CPU、内存、Load 等不同含义的曲线不做无意义合计。

#### 4.2.1 查询预算与手选粒度

- 首期单请求查询一个服务器、范围最多 30d，合法粒度 1m/5m/15m/1h/6h。按 UTC 边界计算桶数（含补齐空桶与两端部分桶），输出上限 **600 桶**；不能只数有数据的行或用结果 LIMIT 截断曲线。
- `auto` 从前述默认粒度开始，预算不满足时向更粗粒度选择。UI 手选粒度显式允许 coarsen；API 的显式粒度默认 strict，只有 `allow_coarsen=true` 才允许改变。strict 超限返回 422 `metrics_query_budget_exceeded`，附推荐粒度；UI 响应含 requested_interval/actual_interval/coarsen_reason 并可见提示，不能悄悄变更。
- 对齐 30d 的 1m/5m/15m/1h 分别约 43,200/8,640/2,880/720 桶，均超过 600；允许 coarsen 时改为 6h，返回 120/121 桶。非法粒度、范围超过 30d 返回 400，不默默缩短 from/to。
- 选定计划最多输入 **12,000 个 raw 样本**，包括 null/reset 样本及必要边界邻居；预算探针使用 `(server_id,time)` 索引与时间 chunk 裁剪，最多读取 12,001 个候选键判断超限，不先做无界 COUNT。最多尝试两个计划，数据查询阶段（探针/读取/聚合）共用 **2 秒**总 deadline，换方案不重新计时。12,000 是计划输入预算，不是数据库内部物理扫描行数的绝对保证；EXPLAIN 与延迟实测另验收。
- 超出 raw 预算且允许 coarsen 时可再尝试 6h rollup 计划；只有转换数据源才减少 raw 输入，不能只扩大 SQL bucket 后仍扫全部 raw。完整闭合桶读取 §8.3 的物化表，两端/未完成桶按有界 raw 回退，所有 raw 来源合计遵守预算。
- 已是 6h、因物化缺口仍需超过 12,000 raw 行时返回 503 `metrics_rollup_not_ready`；其他预算无法满足返回 422；查询总 deadline 到期返回 503 并标明查询超时。不得伪造缺口为 0/null、无限降粒度或放宽扫描保护。30d 尚未回填时先完成回填，不强行全扫约 43,200 行 raw。

以上为首期设计上限，Phase 3 前用 600/601 桶、12,000/12,001 raw、完整/缺失 rollup、两端部分桶和 2 秒 deadline 的测试冻结规则，尚未实测当前数据库性能。

### 4.3 图表交互

- 卡片显示指标名称、单位、当前范围/统计方式和最近采样时间；范围控制在同一页面共享。
- hover 显示时间或 bucket 区间、系列名称/颜色、值及单位；图例能识别并开关系列，轴与 tooltip 换算一致。
- 触摸可固定选中，左右键选择相邻点，Esc 退出；窄屏不依赖 hover 才能查看数据。
- 缺失显示“无采样”并断线，真实 0 显示 0；处理孤立点、全空及错误状态，避免 NaN/Infinity。
- 切换时间范围取消旧请求或隔离迟到结果，快速切换不混入旧数据；刷新保留明确的查询窗口语义。
- 显示最近采样时间与数据新鲜度；后台页暂停周期请求，恢复可见时补拉，失败时保留可识别的过期状态与重试入口。

### 4.4 验收

1. 默认轴上 CPU 5% 不画成接近满幅；切换自适应后标识清晰。
2. RX/TX 同轴同单位，轴标签与 tooltip 一致；真实零与缺采样可以区分。
3. `10, null, 20` 形成断线；缺数据不自动补零，不显示 NaN/Infinity。
4. 已采集 90% 峰值经过历史聚合仍能在“峰值”看到，而平均值单独显示。
5. 时间范围/粒度切换、失败重试、触摸/键盘和 375px 窄屏可用；后台页停止周期请求。
6. 图表不会通过提高刷新频率伪装采集精度；单样本桶可展示已计算的有效相邻速率，没有有效前驱样本时显示缺失。
7. 满足下一节的非纯颜色辨识、对比度、焦点、读屏和数据替代视图要求；自动语义检查与实际读屏验收分别记录。

### 4.5 图表可访问性

| 维度 | 首期要求与验证方式 |
| --- | --- |
| 系列辨识 | 系列名称配合稳定的线型/marker 区分，例如实线圆点、虚线方块、点划线三角；图例、曲线、孤立点与 tooltip 一致。灰度显示下仍能辨识，切换范围/统计方式不改变系列身份 |
| 对比度 | 刻度、图例、tooltip 和状态文字对背景至少 4.5:1；传达信息的曲线、marker、选中状态对相邻背景至少 3:1。亮/暗主题均验证实际颜色与透明度合成结果，装饰网格不套用关键曲线要求 |
| 焦点与键盘 | 清晰且不被裁切的焦点轮廓，产品目标至少 2px、对相邻背景至少 3:1；Tab/Shift+Tab 能遍历范围、统计、轴模式、系列开关、图表选点区域和数据视图，无键盘陷阱。保留方向键、Home/End、Esc；开关暴露 pressed/checked 状态 |
| 读屏 | 每图有名称、范围/时区、实际 bucket、统计方式、单位和新鲜度描述；选点读出系列名、值及单位，0 与“无采样”可辨。用户切换/失败采用简短 polite 提示，自动刷新不反复朗读全部数据或抢焦点 |
| 数据替代视图 | 提供“查看数据”入口与语义 table；caption 标明范围、粒度、统计方式和单位，列包含时间、系列值、有效采样/覆盖信息。与图表共用响应，包含所有展示 bucket 的 null/0，默认每页 50 行，分页可键盘/读屏操作 |
| 缩放与触摸 | 200% 页面缩放、375px 窄屏不遮挡控件；主要触摸目标至少 44px，横向选点不阻止页面纵向滚动 |

TC-04-04 包含自动语义/对比度回归，以及 NVDA + Firefox、VoiceOver + Safari 的人工读屏记录（含版本）。上述要求对应 [非纯颜色辨识](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html)、[文字对比度](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html)、[非文字对比度](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html)和[复杂图像替代信息](https://www.w3.org/WAI/tutorials/images/complex/)；不据此宣称整个产品已完成 WCAG 认证。

## 5. REQ-05：改密码撤销 JWT 与对应终端

### 5.1 行为与方案

- 建议使用 `users.token_version` 与 JWT claim。密码修改事务中更新摘要并原子递增版本；验证必须同时检查签名、有效期与当前版本。
- 改密码成功后，该账号旧 JWT 的新 HTTP 请求返回 401；旧 JWT 不能通过新的 WS 首帧认证。
- 登记已有 JWT 终端的用户/版本，撤销时停止输入转发、关闭 WS/SSH session，回收配额与 worker；记录明确的会话撤销原因。
- 改密码成功后当前浏览器清理认证及敏感缓存，返回登录页；跨标签同步。其他用户不受影响。
- API Key 的 HTTP/WS 会话、scope、ServerIDs 独立管理，不因修改密码自动删除或撤销。
- 首次部署该机制时，缺少版本 claim 的历史 JWT 统一失效并要求重新登录；不默认为当前版本。

“立即失效”指提交成功后新请求不能再通过旧版本，不能依赖长 TTL 缓存慢慢过期。已经执行中的普通 HTTP 操作不保证回滚；已有交互终端必须主动结束。新建终端与撤销并发时，也必须避免会话登记晚于通知而逃过关闭。

首期的同步机制、主库读取与本地回收预算见下文；多实例不属于该保证的验收范围。

### 5.2 注册与撤销的线性化机制

首期按单 Hub 实施，增加按 UserID 共享、可响应 context 的用户 gate，以及 JWT 终端 registry。HTTP JWT 准入、终端注册/ready/输入许可和密码更新使用同一 gate；网络写入、关闭及 worker 等待不持 gate。

同一 UserID 使用稳定的同一 gate，不能在仍有操作时删除并创建另一把锁。锁序为用户 gate → DB 行锁，或用户 gate → registry/会话锁；不得持 registry/会话锁再等待 gate/DB，也不得重复获取同一 gate。cancel 只发非阻塞取消信号，网络 Close/join/done 等待均在锁外。

1. **终端登记**：首帧完成初次 JWT 校验后，进入用户 gate，用新查询再次读取主库 `token_version`；通过后立即登记 `preparing` 会话，包含 session ID、用户/版本、撤销状态、cancel 和 done。登记成功后才开始 SSH 配额等待/拨号，准备中的会话也在撤销集合中。
2. **ready 准入**：SSH/PTY 准备后，在同一 gate 内重新检查主库版本和会话状态，原子完成 preparing→ready 准入，再发放输入许可；已撤销的晚到连接不得获得新的 ready 许可或启动转发。新输入许可与撤销串行，实际写入使用可取消的会话 context；提交前已发送的帧可能晚到，不把客户端接收时间当成准入线性化点。
3. **改密码提交**：新摘要可预先计算；进入 gate 后，在数据库事务中锁定/重新确认当前用户摘要与版本，验证原密码，原子更新摘要并递增版本。**事务 COMMIT 是撤销线性化点**；提交后、释放 gate 前，同步标记旧 JWT 会话 revoked、停止发放新输入许可并调用 cancel，再在 gate 外关闭和 join。
4. **两种顺序**：登记先于 COMMIT 的会话一定在撤销集合中；COMMIT 先于登记时，注册的二次查询一定拒绝旧版本。不能采用“先查版本、通知撤销、稍后无保护地登记”的流程。提交前已取得输入许可或已经在远端运行的命令不承诺撤回。
5. **清理归属**：会话结束后按 session ID 幂等注销，done 表示 WS/SSH/lease/worker 已完成本地清理；只关闭该会话持有的资源，不通过关闭整个服务器连接池撤销用户，避免误伤 API Key 和其他用户。

明确回滚时摘要/版本都不改变。若 COMMIT 报错且结果不确定，在 gate 下标记该用户提交状态未决、取消 JWT 会话并返回可区分的“提交结果未知”；未确认最终提交/回滚前，JWT 准入返回 503。通过有界主库行锁/事务状态核实确认原事务完成后再解除未决状态，单次普通查询读到旧版本不足以证明回滚，不能无限持 gate 等待。Hub 重启后，用户 gate 的首次准入同样通过有界主库加锁读取排除上次未决密码事务，不能靠进程重启恢复放行。

COMMIT 已成功时，即使改密码 HTTP context 已取消，也必须执行撤销标记与取消信号；清理采用独立总截止时间，不能因客户端断开而跳过。3 秒清理预算以本地确认 COMMIT 成功的单调时间为起点，版本撤销的逻辑线性化点仍是数据库提交。密码已提交但清理超时必须分别报告，不把清理失败描述为密码事务回滚；未完成清理不发布 done，也不提前归还仍被 worker 使用的 lease。

### 5.3 版本读取与时间预算

- 首期每次 HTTP JWT 验证、WS 首帧、注册二次校验与 ready 准入均使用主库新查询；禁止读副本、复用旧事务快照或用正向 `token_version`/认证缓存放行。其他用户资料可以缓存，但不能作为版本授权依据。
- gate 等待与版本查询合计预算为 **2 秒**，并受父 context 剩余预算限制；版本不符/历史 claim 缺失返回 401，主库失败或等待/读取超时拒绝准入并返回 503。WS 升级后的失败使用对应协议错误并关闭，不能回退到旧缓存。
- “立即”指 COMMIT 后开始的准入检查不能接受旧版本，版本陈旧放行窗口为 **0**；不是认证耗时为 0。COMMIT 前已经完成准入的普通 HTTP 操作视为在途，不保证回滚。gate/主库检查的实际延迟、DB 查询量与失败数须测量并记录。
- 旧 JWT 终端从本地确认 COMMIT 成功起 **3 秒内**完成本地 WS/SSH/lease/worker 回收：最多 2 秒优雅关闭，再留最多 1 秒强制关闭该会话 transport 并 join。总截止时间不能在多个清理阶段重新开始；超预算记录独立失败并保留未完成状态，不能假报资源已回收。
- 以上为待实现的验收预算，当前终端 grace 后仍可能无界等待，不视为已满足。首期不引入版本缓存；未来若需要，须另外证明同步失效、晚到读取不能回填旧版本及重启语义，不能只增加 TTL。

**正常延迟目标**：健康基线下，JWT 准入的 gate 等待、DB 连接池等待和版本查询合计 p95 ≤ 20ms、p99 ≤ 50ms；2 秒是故障截止预算，不是正常性能目标。基准条件为 Linux/SSD、至少 4 vCPU/8 GiB 的测试资源、单 Hub 与主库同机或 RTT ≤ 2ms，预热 30 秒后持续 5 分钟，以同账号有效 JWT 25 请求/秒、最多 8 并发访问无其他 DB 业务查询的认证端点。记录实际资源配额、版本、连接池、样本数、查询数和分位延迟；该配置是测试基线，不是部署最低配置。主动改密码与故障另测正确性/2 秒拒绝预算。以上目标尚未实测，Phase 3 前提交测量报告并冻结性能阈值，不能通过缓存版本来达标。

多 Hub 的进程内 gate 不构成跨实例同步。本次验收限定单 Hub；在设计并验证跨实例协调/撤销传播之前，不宣称多实例满足同等会话撤销保证。

### 5.4 验收

1. 正确原密码修改成功：所有旧 JWT 的后续请求拒绝，新密码登录的新 token 可用，旧密码无法登录。
2. 原密码错误、确定未提交的 DB 失败和已确认回滚：密码/版本均不改变；并发修改不能覆盖版本。COMMIT 结果未知按 §5.2 单独处理。
3. HTTP 和 WS 首帧都验证版本；旧 WS 被关闭，SSH 配额释放，撤销与新建竞态有回归用例。
4. 当前浏览器/其他标签清理缓存；API Key 仍按原权限工作，其终端不被误关。
5. 升级时历史 JWT 行为明确；测试不把“删除浏览器 token”当作后端撤销。
6. 用固定时序暂停在初次校验后、registry 登记后、SSH 准备后/ready 准入前，并发改密码；不存在遗漏撤销或提交后新发放的旧版本 ready/输入许可。
7. 主库失败/超时及预先存在的旧用户缓存不能放行；确定回滚、结果未知、已提交但清理失败有不同结果语义。阻塞对端验证 3 秒总清理预算。

REQ-05 的 schema/JWT/HTTP 部分可并行开发；终端撤销完整验收在 REQ-06 公共 lease/取消契约和 REQ-07 teardown 预算完成之后，不能仅凭 registry 测试标记整项完成。

## 6. REQ-06：Agent 上传资源与取消修复

### 6.1 要求

- 成功借用后，所有正常/错误出口都恰好归还一次配额；健康 client 可复用，损坏 transport 丢弃。
- 统一 SSH lease，绑定 entry/generation 和归还权；验证旧连接不能归还到新指纹条目。
- 文件读取、session 建立、传输、远端执行有总 deadline，响应父 context 取消；关闭流并回收上传 worker。
- 传输失败不能仅记录警告后报告成功；Agent 上传失败不拖住全部节点后续采集。

资源层由 `SSHService` 及其 lease 原语唯一负责：在固定目标/凭据/generation 快照下执行命令和受限上传，汇总本地读取、复制 worker、远端退出与取消错误，负责恰好一次归还或丢弃。原语允许复用同一 lease，不在内部嵌套借用；声明传输阶段预算及有界 teardown。REQ-06 不实现 Agent 架构/版本判断，也不改 `collectOne` 的部署编排；该调用迁移由 REQ-08 唯一负责。先冻结借用、执行、上传、cancel、done、release/discard 的契约及错误分类，再分别实施。

### 6.2 验收

1. 连续上传次数超过配额上限后，Exec/Terminal/采集仍可正常借用。
2. 本地文件失败、session 失败、远端拒绝、复制中断、阻塞对端和取消都在 §7.1.1 的本地清理总预算内回收配额和 worker；连续失败不降低可用配额。
3. 重复或竞争清理不会二次归还、死锁；变更主机/凭据后的旧连接不用于新参数。
4. 用协议模拟固定并发时序验证指纹竞态；修复后执行相关 race 检查。

## 7. REQ-07：统一执行与传输超时

### 7.1 要求

- 分别定义请求总预算、SSH 配额等待、拨号/握手、业务执行、传输写入和清理预算，错误能区分具体阶段。
- 保留普通 CRUD 的合理读写保护；长 Exec 和 Relay 使用路由适配的写 deadline，不能全部沿用 15 秒。
- Exec 未传 timeout 时使用有效 `EXEC_TIMEOUT` 配置；明确请求覆盖、默认和上限，避免 Handler 硬编码覆盖配置。
- 保留最长 300 秒执行能力，并给排队/连接/结果返回预留清晰预算；到期立即触发取消，在明确清理预算内关闭本地资源并等待 worker，不继续无限等待。
- Relay 区分普通请求与受限长流，不宣称支持无限 SSE 或 101/WS 升级；写入失败、上游超时和客户端取消可识别。
- 明确反向代理 timeout 配置要求；HTTP socket 写 timeout 不等价于业务 context 取消。
- 设置 Exec 输出总/单流字节预算，返回清晰截断标志；限制相关请求体大小，防止合法长操作无界占用内存。
- 阶段有效预算取“阶段上限与请求剩余预算”的较小值；使用单调时钟测耗时，清理有独立且不反复延长的总截止时间。JWT 终端撤销采用 REQ-05 的 3 秒总清理目标；其他操作把有效预算写入用例环境与统一遥测，不能把 grace 后无界等待算作有界回收。

以下数值为已冻结的首期实施契约，不再保留候选值；尚未实现或完成性能测量不等于值未定。后续调整须同步修改配置约束、需求与验收边界。

#### 7.1.1 清理默认值

| 操作 | 优雅清理 grace | 本地清理总预算 | 计时起点 |
| --- | ---: | ---: | --- |
| Exec、Agent 探测/上传/部署/采集的 SSH 链路 | 100ms | 3s | 本地确认失败或收到取消信号 |
| 普通终端断开/取消 | 2s | 3s | 本地开始关闭 |
| JWT 撤销终端 | 2s | 3s | 本地确认密码 COMMIT 成功 |
| Relay 取消/写入失败 | 100ms | 3s | 本地确认失败或收到取消信号 |

grace 是总预算内先尝试正常关闭的阶段；到期强制关闭该操作独占 transport/流，再在剩余期限内 join。部署多个步骤与 Monitor 调用共用一个清理截止时间，不按阶段或层次重新开始 3s。尚未获得 lease 的调用只回收自己的等待状态；超限不发布 done、不提前归还仍被 worker 使用的配额。临时文件清除/远端进程退出不能与本地回收等同，失败单独记录。Exec 的 100ms 基于现有 grace，其余统一值及总预算是待实施设计默认值；测试从此表取值并记录调度容差。

#### 7.1.2 Exec 输出保存限额

| 配置/约束 | 首期固定默认值与上限 |
| --- | --- |
| EXEC_OUTPUT_LIMIT | stdout + stderr 总保存量 8 MiB（8,388,608 原始字节） |
| EXEC_OUTPUT_STREAM_LIMIT | stdout、stderr 各最多 4 MiB（4,194,304 原始字节） |

允许启动环境将两项降低，不允许超过上表上限或由请求覆盖；有效值必须为正整数且单流不大于总量，非法配置在启动校验时拒绝。并发写入共享线程安全的总保存量计数。

达到限额后继续读取并以常量空间丢弃，直到真实退出或原执行 deadline，不因截断取消命令或延长执行期限，避免输出背压。只有实际丢弃字节才设置 output_truncated/stdout_truncated/stderr_truncated；恰好达到上限随后 EOF 不算截断。返回各流 retained/discarded 字节数，保留真实退出码：exit 0 仍为 Usage succeeded，非零仍为 failed/ssh_nonzero_exit，截断作为独立元数据；超时/取消保留原原因，未知退出码不得填 0。

8 MiB 是保存的原始字节上限，不是编码后 JSON 大小或整个进程 RSS 上限。TC-07-02 覆盖限额−1/恰好限额/限额+1、单流与双流并发、持续大输出后退出/超时/取消，确认保存量有界、内存不随累计产量无限增长及结果语义正确。

#### 7.1.3 Relay 模式与受限长流预算

Relay 外层请求新增 mode=standard/bounded_stream，省略为 standard、非法值返回 400；该控制字段不转发给上游。继续使用 services:relay 权限，不依据 Content-Type/Accept 或上游行为自动升级，不自动重试已经发出的请求。

| 模式 | 业务总预算 | 上游响应头等待上限 | Body 无进展/下游写阻塞上限 |
| --- | ---: | ---: | ---: |
| standard | 30s | 30s | 30s，且不超过剩余总预算 |
| bounded_stream | 300s | 30s | 30s，且不超过剩余总预算 |

业务总预算从 Handler 开始业务处理计时，包含查库、凭据处理、连接和复制；阶段取剩余期限，不叠加成 330s。无进展指没有成功向下游转发 body 字节，空 flush 不续期；有字节心跳可以更新 idle，但不能重置 300s 总截止。首次 body 等待也受 30s 保护，读取与下游阻塞写均可取消，长流使用适配的 HTTP 写 deadline，不能仍被 30s client timeout 或 15s 全局写 timeout 截断。清理另按 §7.1.1 的 100ms grace/3s 总预算，禁止无限 SSE/101 Upgrade，保持禁止自动重定向。

响应头未发送时，上游总超时/idle 超时可返回 504；响应头已发送后只中止传输并记录真实上游状态、已复制字节、失败阶段，不追加第二个 JSON 或改写状态。未知长度流不能把预算到期当正常 EOF，不能记为完整成功。TC-07-03 以可控时钟验证普通 30s、显式长流跨 30s 正常传输、300s 总截止和 30s idle/写阻塞边界，并覆盖响应头已发送后的失败。

本地 SSH session/transport 关闭不保证远端任意命令的全部子进程已经终止。本期验收本地资源和结果语义；不把“连接已关闭”报告成“远端进程树已全部退出”。

### 7.2 验收

1. 20–30 秒正常 Exec 能完整返回；未指定 timeout 时自定义默认生效，超过上限有一致处理。
2. 排队、拨号、运行、响应写入、客户端断开分别可控测试；完成后配额及 worker 回收。
3. Relay 在承诺期限内持续传输；有限长流到期有一致行为，正常 CRUD 保护未丢失。
4. 大量输出不会无限增长内存，截断不伪装成完整结果；输出限制与退出码/Usage 结果语义一致。
5. TC-06-03/TC-07-04 注入重复 cancel、强制关闭、嵌套部署/Monitor 调用和慢 join，断言绝对 3s 清理截止未重置；100ms/2s grace 计入该 3s，而不是额外叠加。超限如实报告，不发布 done 或提前归还仍被 worker 使用的 lease。

## 8. REQ-08：监控生命周期、采集程序上传/更新与指标正确性

### 8.1 调度和退出

- 后台 Monitor 接入 App 根 context，退出时停止调度、取消在途工作并等待回收，再关闭连接和存储依赖。
- 节点独立调度，有全局并发上限、每节点防重入、总 deadline 和失败 backoff；慢节点不拖住全部节点。
- 在线判断与有效采集周期、容错轮数和延迟联动，保留数据新鲜度，避免固定 120 秒阈值误报。
- 记录成功/失败/超时、采集耗时、延迟、配额等待和漏采，能够识别达不到现有配置周期。

#### 8.1.1 已冻结的调度数值与退避规则

- 全局在途采集上限为 **min(N,16)**，N 为当前管理节点数；N=0 时不启动采集。每节点最多一个在途任务和一个待调度记录，总待调度记录不超过 N，不为每个错过的 tick 堆积任务。公平准入，无整批等待屏障；节点数下降后停止新准入直至收敛，删除节点取消其任务。
- 节点准入后总采集 deadline 固定 **30s**，覆盖 SSH 配额等待、探测、必要部署、执行、解析与写库；各阶段取剩余预算，清理另遵循 §7.1.1 的统一截止。全局排队阶段不占 worker/SSH lease，等待时长单独记录。慢节点仍会占用有限容量，不承诺容量耗尽时其他节点无需等待。
- 保持现有合法正整数 MONITOR_INTERVAL=I，启动时验证 I>0；不新增页面频率设置。连续失败次数 k≥1 时，cap=max(I,300s)，base=min(I×2^(k−1),cap)，取均匀 jitter∈[-0.1,0.1]，实际退避为 clamp(base×(1+jitter),I,cap)。使用饱和计算避免指数溢出，从本次清理完成起计时；退避不占 worker/lease。
- 实际部署/采集/协议/写库失败和采集自身 deadline 到期累计 k；应用退出、节点删除、generation 失效引起的取消不累计。排队/防重入跳过记漏采或迟到，不提高失败次数。成功后 k=0，恢复下一个未来正常计划时间，不补跑历史 tick；任务执行超周期时不重入、不连续补偿。
- TC-08-01 用可控时钟与固定 RNG 断言：零 jitter、I=60s 时失败延迟为 **60/120/240/300/300s**；I=600s 时不会小于 600s；jitter 保持范围，成功清零，各取消分类不累加。17 节点最多 16 在途、单节点不重入、待调度≤N、退出/删除后资源回收均可测。以上值是首期实施契约，当前代码尚未实现。

### 8.2 监控采集程序的上传与更新（Agent 分发）

这里的 Agent 是 Talus 自带的 Linux 指标采集 CLI，不是 AI Agent。“分发”指 Hub 通过现有 SSH 链路将适配目标 CPU 架构的程序上传到被管理服务器，并在版本变化时更新。每次采集时执行程序、读取 JSON，程序随后退出；二进制文件保留供后续复用，不新增常驻服务或采集监听端口。

当前 Hub 将本地 `/usr/local/bin/vpsmanager-agent` 上传为目标 `/tmp/vpsmanager-agent`；只在缺失时上传，没有目标架构选择与版本更新机制。例如 amd64 Hub 管理 arm64 服务器时，其镜像内的 amd64 Agent 无法直接在目标执行。目标流程为“识别架构 → 选择匹配产物 → 判断版本/hash → 必要时上传、校验并原子替换 → 按次执行采集”。REQ-06 修复上传资源与取消；本项完善上传哪个程序、何时更新及如何安全替换。

- Hub 支持识别目标 linux/amd64、linux/arm64，携带对应 Agent；不支持架构明确报错。
- Agent 产物有可识别版本或内容 hash，用于更新与完整性检查；另有明确的协议兼容策略，避免把 hash 等同协议版本。存在旧文件时能判断更新，Hub 升级不能一直复用旧二进制。
- 临时文件上传校验后原子替换；激活前的确定失败不留下看似有效的部分产物、不覆盖可用旧版本；激活结果未知单独核实。
- 明确写入/执行目录与权限，处理 `/tmp` noexec、只读或缺少权限；清理上传临时文件。可保留版本化 Agent 缓存，并如实说明无常驻采集进程。

#### 8.2.1 上传前架构探测与错误分类

- 在任何 Agent 上传或执行前，通过已验证主机密钥的 SSH 连接分别执行固定命令 `uname -s`、`uname -m`；先确认 Linux，再将去除首尾空白后的 `x86_64` 映射为 `linux/amd64`、`aarch64` 映射为 `linux/arm64`。其他平台/架构明确拒绝，不能默认采用 Hub 架构或先上传试运行。
- 探测阶段最多 5 秒，受整体剩余 deadline 限制。SSH 配额/拨号/认证/主机密钥错误保留资源层分类；另区分探测超时/取消、命令非零退出、空/非法输出、不支持平台/架构和本地匹配产物缺失。这些情况下上传次数必须为 0。
- 不依赖 Agent 输出的 `kernel_arch` 做首次选择；错误架构程序可能无法启动。首期每次采集先探测，不新增持久化架构缓存；未来缓存至少绑定主机密钥、连接指纹和 generation，失效时重新探测。

#### 8.2.2 多架构产物与 Hub 镜像

- 采用 Docker 构建阶段的 Go 交叉编译：Hub 继续使用 `TARGETOS/TARGETARCH`；Agent 独立以 `CGO_ENABLED=0 GOOS=linux` 分别构建 `GOARCH=amd64` 与 `arm64`，两份产物使用相同源码版本。首期不引入 GoReleaser 或用 build tag 代替目标架构选择。
- 每一种 Hub 镜像均包含 `/usr/local/lib/talus/agents/linux/{amd64,arm64}/vpsmanager-agent` 和 `/usr/local/lib/talus/agents/manifest.json`；清单含 platform、build version、protocol version、SHA-256、size。Hub 启动时校验清单与本地产物，清单缺失/解析失败、任一必需产物缺失或架构/size/hash/协议不匹配采用 **fail-fast**：非零退出，不启动 HTTP 监听或 Monitor。首期不增加隐式单架构降级；本地开发也须通过统一入口准备产物。远端节点错误只使该节点采集失败，不退出运行中的 Hub。
- 根 `Dockerfile` 与 `backend/Dockerfile` 复用拟新增的 `backend/build/agents.sh` 构建/清单生成入口，两者均以仓库根目录为 build context；CI 也复用这一入口。REQ-08 负责该逻辑与 CI 的接入，避免两个 Dockerfile 各维护一套架构清单。
- CI 分别验证 amd64/arm64 Hub 镜像**内部均含两份 Agent**，检查 ELF 架构、size/hash 和协议 JSON 烟测；可用原生 runner 或明确配置的 QEMU。仅发布双平台镜像 manifest 不算完成异构管理验收。

#### 8.2.3 唯一归属与调用契约

新增 `AgentDeploymentService`（暂定名，位于现有后端 service 层），作为“探测 → 选择 → 远端检查 → 上传 → 校验 → 激活 → 执行采集”的唯一业务 owner。它接受 context/server ID，返回采集输出与实际产物身份；Monitor 仅负责调度、指标校验及入库，不再内联缺文件上传。REQ-08 独占 `monitor.collectOne` 中部署逻辑的迁移，REQ-06 只改公共 SSH 传输/资源层。

| 组件/需求 | 唯一职责 | 依赖契约 |
| --- | --- | --- |
| SSHService / REQ-06、REQ-07 | lease、目标快照、命令/上传原语、取消、错误汇总、归还/丢弃和清理预算 | 提供可在同一 lease 上复用的原语，不解释 Agent 版本/架构 |
| AgentDeploymentService / REQ-08 | 架构/产物选择、部署去重、完整性/协议、原子更新与运行 | 使用 SSHService 原语，不直接操作 pool 或另写上传 worker |
| Monitor / REQ-08 | 节点调度、调用采集服务、解析校验与持久化 | 接收可分类失败，失败不写入伪造指标 |

同一目标/generation 的探测、更新和执行使用一份 lease，避免每一步重新借用、混用变更前后目标。同节点并发部署串行/合并，在途调用遵守各自取消；激活/执行前在本地短临界区确认 generation 有效，失效后不再准入新阶段并取消在途工作，网络操作不持该锁。失效前已获许可/发送的远端操作仍可能完成，不能保证撤回 rename。连接健康时归还，损坏时丢弃，旧 generation 不进入新缓存。

远端检查使用主机自带的文件/校验工具读取选定路径的存在状态和 SHA-256，不靠运行不兼容的旧 Agent 来判断更新，也不以英文 stderr 子串充当完整部署状态机。临时文件放在最终文件的同一文件系统，名称唯一；上传 → 校验大小/hash → 设置权限 → 原子 rename，确认激活成功后才执行。激活前的确定失败保持旧版本可用并在预算内清理临时文件；`/tmp` noexec、只读、缺少校验工具或权限等有独立原因分类。具体可执行目录由部署配置明确，不自动猜测或切换目录。

激活命令已发出但响应丢失时归类为“结果未知”；恢复连接后重新验证目标身份/架构及远端实际 hash，确认前不宣称旧版仍活动、不盲目覆盖重试、不写入指标。激活后的执行/协议失败不等于旧版仍活动；运行结果必须验证协议版本，未知协议拒绝入库，首期不承诺自动回滚。

#### 8.2.4 采集 JSON 的版本契约

`cmd/agent` 的 `--format json` 输出必须在既有采集字段外携带顶层 `agent_version`（非空构建标识字符串）与 `protocol_version`（整数，首版固定 1）。两架构构建注入相同 agent_version，manifest 与运行输出共用协议定义；Hub 解码结构显式接收并校验这两个必需字段，再校验指标入库。

首期 Hub 仅支持 `protocol_version=1`，采用整数精确匹配，不引入主/次版本猜测。兼容新增可选字段时保留版本并允许未知可选字段；修改字段含义/类型等破坏兼容性时提升协议整数，由 Hub 明确增加支持解析器后才能接受。运行 agent_version 必须与选定 manifest 的 build version 一致；SHA-256 用于产物身份/完整性，不能代替运行协议验证。

缺版本字段的旧 JSON 视为 legacy，不默认成协议 1；字段类型错误、空 agent_version、未知协议或构建身份不符明确失败、不入库。升级先用 hash 检查并替换旧二进制，随后运行新格式，已有数据库历史指标不因缺少这些输出字段而删除。TC-08-05 保留构建产物烟测；TC-08-08 专门验证启动 fail-fast、运行协议精确匹配/legacy 拒绝及零入库，详细场景见 §10.1。

### 8.3 指标与历史

- 网络/磁盘先按相邻样本计算 delta/time，再按图表桶聚合；查询起点需取得前驱样本，按有效覆盖时间加权聚合，并单独保留已采样区间速率峰值。
- 正常递增且有效时间差下计算速率；计数器下降/reset、零/负时间差或超过声明断采阈值的区间不参与有效速率聚合，返回未知值并形成缺口，不能补零或跨长缺口平滑。真实零增量仍表示零吞吐。阈值与有效采集周期关联，验收 fixture 写明取值。
- 时间来源统一，明确 Hub 接收时间与 Agent 采样时间；不能用零替代未知值。
- metrics 按下列数值基线实施保留和长窗口聚合；平均与峰值分别保留，不提前假定压缩收益。
- 图表/API 契约覆盖时间范围、粒度、统计方式和实际采样分辨率；对超大查询设边界，UI 与后端一致。

#### 8.3.1 保留、聚合与清理基线

首期选择自维护 `metrics_rollup_6h` 表与持久化物化检查点，暂不使用 continuous aggregate。桶以 `(server_id,bucket_start)` 唯一定位，保存下述统计状态；桶结果和完成检查点在同一事务提交、重算幂等，失败不推进检查点。选择依据是统一处理相邻计数器/有效时长权重/空桶完成证明与恢复，不是断言 TimescaleDB 无法实现这些计算。

| 项目 | 首期数值/行为 |
| --- | --- |
| 原始数据 | 默认 60 秒采集，逻辑保留 35 天，覆盖 30d 窗口并留下 5 天重算余量；不恢复已取消的采集频率需求 |
| 长窗聚合 | 6 小时 bucket，逻辑保留 35 天，按 UTC/Unix epoch 边界对齐；其他粒度从保留的 raw 查询，但受 §4.2.1 的 600 桶/12,000 样本/2s 保护约束，超限按规则降粒度或返回 422/503，不能保证 30d 细粒度查询成功 |
| 聚合刷新 | 每小时执行，正常重算最近 48 小时；首次部署回填可用的最近 35 天，失败恢复时按检查点补齐遗漏范围，超过正常窗口的补录显式 backfill |
| 查询拼接 | 请求两端的部分桶、未闭合桶和尚未物化的闭合桶按请求边界从 raw 算；仅范围内完整且已物化桶走 rollup。来源区间互斥，统一 `[start,end)`，避免混入范围外样本、遗漏或重复 |
| 清理 | 每天执行一次，按当前 1 天完整 chunk 删除；删除前确认该 chunk 所需聚合完整，失败时暂停删除并产生失败事件 |
| 物理边界 | 健康清理时逻辑 35 天加至多约 2 天的 chunk/调度余量；清理失败会增长，必须告警，不假定始终有硬容量上限 |

6h 聚合保存各指标的有效 count、sum/权重状态和已采样 max；I/O 另保存有效覆盖时间及增量或等价加权状态。理想连续采集的闭合桶有 360 个样本，实际 count/coverage 如实返回。持久化物化检查点覆盖已处理区间，包括明确确认的空桶；“聚合完成”不要求每桶实际采满 360 点，也不能只依据存在几行聚合记录判定。不能平均多个均值、把缺失当 0，或在删除 raw 后重新用空数据覆盖已保存的聚合。retention job 的刷新范围必须与 raw 保留边界协调，旧物化数据只按自身保留策略删除。

UTC 边界对齐的完整 30d 窗口对应 120 个 6h 桶；滚动窗口可能含 121 个桶及两端部分覆盖，响应明确实际边界。实施验收用可控时间/历史 fixture 验证 35d 清理、聚合重算、无数据缺口和迁移幂等，不等待 35 天才测试。

#### 8.3.2 存储量估算与实测

行数公式：`节点数 × 86400 / 采集秒数 × 保留天数`；聚合行数将采集秒数换成 bucket 秒数。每节点 60 秒采集时，30 天为 **43,200 行**，35 天为 **50,400 行**；6h 聚合分别为 **120 / 140 行**。

容量规划暂以原始“表+索引”每行 **0.5–1 KiB**、聚合每行 **1 KiB**估算，未计压缩收益：

| 节点数 | 35d 原始行数 | 35d 聚合行数 | metrics 表/索引估算 |
| --- | ---: | ---: | ---: |
| 1 | 50,400 | 140 | 约 25–50 MiB |
| 100 | 5,040,000 | 14,000 | 约 2.4–4.8 GiB |

这是行宽假设下的估算，不是当前数据库实测；物理 chunk 余量、Usage、WAL、备份和复制槽另计。metrics 磁盘规划先留稳态估值 2 倍空间，实施时记录至少 1d/7d 代表性数据的实际表/索引/全部 chunk 大小、行数、每行有效字节及清理前后变化，再外推 35d。字符串长度、NULL、索引与 MVCC 都会改变行宽，不能把 Go 结构体大小当数据库大小。

### 8.4 验收

1. 退出/重启后不遗留采集 worker，慢节点不会卡住所有节点；节点任务创建与回收不启动重复任务。
2. 同一 Hub 管理 amd64/arm64 测试节点，版本更新、校验失败、noexec/权限错误可识别；不兼容协议明确拒绝，不写入误解析指标。
3. 稳定递增、归零重启、断采、零吞吐和相同时间戳用例得到合理速率，无除零/假峰值。
4. 在线/过期阈值按现有配置周期、容错轮数与允许延迟计算；阈值内在线，超过阈值显示离线/过期，不再写死 120 秒。长范围采样峰值与保留策略可验证。
5. 架构探测、未知平台、超时或匹配产物缺失时上传为 0；原 SSH 错误与探测错误不被混成“不支持架构”。两种 Hub 镜像分别通过双 Agent 产物检查。
6. 激活前上传/校验/chmod 及明确未执行的 rename 失败保留旧版本，部分文件未激活、临时文件回收；generation 在激活准入前失效时不发 rename，已发激活后断链的结果未知通过实际 hash 核实。
7. 35d 保留与 6h 物化可核验；清理失败不删除尚未聚合数据，30d 结果与原始样本的平均/max/coverage 对照正确，并提交容量实测记录。
8. TC-08-08 验证本地产物错误启动非零退出、HTTP/Monitor 未启动；运行协议 1 且构建身份匹配才可入库，legacy/错误类型/未知协议/版本不符均零入库，远端失败不退出 Hub，历史指标保留。

## 9. REQ-09：继续使用 xterm 优化终端 UI

| 首期功能 | 要求与验收 |
| --- | --- |
| 信息与状态 | 顶栏展示服务器名、host:port、连接状态及重试次数；失败原因在终端外可读 |
| 连接控制 | 手动连接/断开/重连、取消重连；区分连接中、重连中、断开和失败；重连表明新会话，不暗示原 shell 恢复 |
| 布局与全屏 | 终端占主要可用高度，桌面支持全屏，窄屏折行/更多菜单；无整页水平溢出 |
| 字体与主题 | 调整字号并重置；显示偏好可本地记忆；终端/工具栏对比度清晰 |
| 搜索 | 使用 xterm 官方搜索能力，搜索历史缓冲、上/下匹配、Esc 关闭；不向远端执行搜索命令 |
| 剪贴板与清屏 | 显式操作/快捷键说明，复制选中文本，粘贴有权限反馈；多行粘贴提示；清屏仅改变客户端显示 |
| 尺寸与焦点 | 侧栏/全屏/窗口变化后 fit，并防抖同步 PTY cols/rows；查看历史/选中文本时不强制滚底或抢焦点 |
| 移动端 | 375px 及横屏可用，支持软键盘输入，主要控件触摸高度至少 44px |
| 生命周期 | 切主机、连续重连、卸载、密码撤销后没有重复 WS/handler/timer，token 不放 URL，断连回收配额 |

首期为单连接工作区。多标签、分屏、布局恢复和移动 Ctrl/Alt 扩展栏作为后续可选，不在本次首期验收中。仍使用现有 xterm/WS/SSH PTY 链路，参考 [xterm 官方插件](https://github.com/xtermjs/xterm.js#addons) 与 [Tabby 交互设计](https://github.com/Eugeny/tabby)，不引入完整替代终端后端。

## 10. 验收场景摘要（非执行清单）

The following table is the minimum planned inventory for `tests/cases/`. These cases are not claimed to be implemented or passing. Map existing tests into the index first; add new cases for observable behavior and concurrency boundaries.

This table is a human-readable scenario summary. Test level describes the verification approach and does not select a suite. Resolve Suite, Environment, Implementation, Command, and Automation status through `tests/coverage.json`, the sole machine source specified in Section 3.2.1. The generated `tests/coverage.md` is a read-only report.

| Test case ID | Requirement | Scenario | Test level |
| --- | --- | --- | --- |
| TC-01-01 | REQ-01 | Remove adapters while retaining the skill and valid installation links. | Static / Documentation |
| TC-01-02 | REQ-01 | Preserve the service catalog and Relay contracts required by the skill. | API / Integration |
| TC-01-03 | REQ-01 | Reject residual adapter artifacts, stale installation links, missing skills, and invalid scan conditions; allow reviewed historical records. | Tooling / Static fixtures |
| TC-02-01 | REQ-02 | Index existing tests with English descriptions, generate coverage.md deterministically from coverage.json, reject report drift, and propagate runner failures. | Tooling / CI |
| TC-02-02 | REQ-02 | Validate JSON schema, unique IDs, references, suites, statuses, and commands before execution; fail on missing dependencies and distinguish mock UI from real E2E. | Tooling / CI |
| TC-04-01 | REQ-04 | Apply correct time ranges, aggregation intervals, axes, and units; distinguish zero from null without implying finer sampling. | Frontend logic / UI |
| TC-04-02 | REQ-04 | Compute averages and observed sample maxima correctly; retain maxima in long-range data. | Repository / Charts |
| TC-04-03 | REQ-04 | Ignore stale responses during rapid range changes; support touch, keyboard input, and visibility-aware refresh. | UI |
| TC-04-04 | REQ-04 | Distinguish series without color, meet contrast thresholds, preserve visible focus, and expose equivalent chart data to keyboard and screen-reader users. | UI / Manual accessibility |
| TC-04-05 | REQ-04 | Enforce 600-bucket and 12,000-raw-sample boundaries; share one 2-second deadline across probes and plans; verify strict/coarsen behavior and 422/503 errors without range truncation. | API / Repository / UI |
| TC-05-01 | REQ-05 | Commit or roll back password and token-version changes atomically; reject old JWTs and accept newly issued JWTs. | Go / API / Database |
| TC-05-02 | REQ-05 | Reject revoked JWTs during WS authentication and close existing sessions, including registration races. | Protocol / E2E |
| TC-05-03 | REQ-05 | Clear authentication across tabs, keep API Keys independent, and reject legacy JWTs without a version claim. | API / UI |
| TC-05-04 | REQ-05 | Serialize registration, ready admission, and revocation around COMMIT; cancel preparing sessions and reject new ready or input permissions for revoked versions. | Go / Protocol / Race |
| TC-05-05 | REQ-05 | Read the primary database without positive version caching, enforce authentication budgets, and reject admission during unresolved commits, including restart recovery. | Go / API / Database |
| TC-06-01 | REQ-06 | Release upload leases on success and every error path; preserve capacity after repeated uploads. | Go / SSH simulation |
| TC-06-02 | REQ-06 | Recover from blocked or canceled uploads and prevent stale-generation lease returns. | Go / Race |
| TC-06-03 | REQ-06 | Reuse one target-pinned lease, propagate copy errors, and reclaim resources within one absolute 3-second deadline with a 100-ms grace; nested deployment and Monitor stages never reset it. | Go / SSH simulation |
| TC-07-01 | REQ-07 | Return complete results for 20–30-second commands and honor the configured default timeout. | HTTP / SSH integration |
| TC-07-02 | REQ-07 | Enforce an 8-MiB retained-output total and 4-MiB per-stream limits, distinguish exact-limit from discarded output, preserve true exit status, and reclaim resources after timeout or cancellation. | Go / Protocol |
| TC-07-03 | REQ-07 | Enforce explicit ordinary/stream Relay modes with 30/300-second totals and a 30-second progress idle limit; handle header/body timeouts, write failures, and cancellation. | HTTP integration |
| TC-07-04 | REQ-07 | Verify 100-ms SSH/Relay and 2-second terminal graces inside one absolute 3-second teardown deadline; preserve the original outcome and never reset budgets or falsely publish done/release. | Go / Protocol / Telemetry |
| TC-08-01 | REQ-08 | Enforce min(nodes, 16) active collections, one in-flight task per node, bounded pending work, capped jittered backoff, cancellation exclusions, successful recovery, and shutdown cleanup. | Go / Scheduler / Race |
| TC-08-02 | REQ-08 | Select architecture-compatible Agents, check version/hash, update atomically, report permission failures, and reconcile unknown activation outcomes. | Agent / Integration |
| TC-08-03 | REQ-08 | Calculate adjacent-sample rates correctly across resets, gaps, and duplicate timestamps; retain observed maxima. | Repository / Logic |
| TC-08-04 | REQ-08 | Probe the target OS and architecture before upload; preserve failure classes and perform zero uploads when probing or artifact selection fails. | SSH simulation / Integration |
| TC-08-05 | REQ-08 | Verify both Agent architectures, manifest hashes, and protocol output inside every amd64 and arm64 Hub image built from either Dockerfile. | Build / CI |
| TC-08-06 | REQ-08 | Enforce 35-day retention and six-hour rollups, trim rolling-window partial buckets, recover aggregation gaps beyond 48 hours, preserve statistics, and measure storage costs. | TimescaleDB / Migration |
| TC-08-07 | REQ-08 | Correlate scheduled collections and deployment attempts, count failures and skips, and emit alerts at the defined lag and retention thresholds. | Go / Scheduler / Telemetry |
| TC-08-08 | REQ-08 | Fail startup before HTTP/Monitor on invalid bundled artifacts; accept only matching protocol/version JSON, reject legacy output without metric insertion, and preserve node isolation and history. | Startup / Agent / Integration |
| TC-09-01 | REQ-09 | Provide usable toolbar controls, search, font sizing, fullscreen, clipboard actions, and client-side clearing. | UI |
| TC-09-02 | REQ-09 | Support 375px and landscape layouts, PTY resizing, stable focus, and reconnect cleanup. | UI / Protocol |

### 10.1 Explicit acceptance boundaries for the new decisions

**TC-08-08 — Startup bundle validation and runtime protocol validation**

- Scenario A: Given a bundle with a missing/invalid manifest, missing required binary, wrong architecture, size/hash mismatch, or unsupported manifest protocol, when starting the Hub, then startup exits nonzero before HTTP listening or Monitor startup and reports the specific failure. Run each fixture independently.
- Scenario B: Given a valid bundle and controlled Agent JSON fixtures, when collecting with protocol_version=1 and the matching agent_version, then valid metrics are inserted. Missing fields, invalid types, unsupported protocol, or mismatched build identity each insert zero metrics; legacy output is not treated as protocol 1. A remote-node failure leaves the Hub running, and existing historical metrics remain unchanged.

**TC-04-05 — Query budgets and response semantics**

- Given 600/601-bucket and 12,000/12,001-raw-sample fixtures, verify inclusive budgets, strict 422 rejection, explicit coarsening to 6h, and 503 for unready rollups or query deadline expiry. Never truncate the requested range or invent missing data.
- Delay the first probe/plan with a controlled clock, then attempt the second plan; both probes, reading, and aggregation share the original 2-second deadline. Starting the second plan never grants a new 2 seconds.

**TC-06-03 / TC-07-04 — Absolute teardown deadlines**

- Cover a 100-ms SSH/Relay grace and a 2-second terminal grace inside the same absolute 3-second local teardown budget, using each operation's defined start event.
- Inject repeated cancellation and nested cleanup calls across Monitor, deployment, and SSH layers; the deadline remains unchanged. All completed workers release their leases exactly once; gauges return to the fixture baseline. An over-budget worker must not publish done or release a lease it still uses, and cleanup must not overwrite the original business result.

Detailed automated/manual implementations remain planned and will be recorded in coverage.json; these descriptions do not claim that tests exist or have passed.

## 11. 交付顺序与完成标准

1. **整理与契约基线**：REQ-01、REQ-02；建立静态检查与英文用例索引，冻结公共 lease/cancel/done、JWT registry 和第 12 节遥测字段/枚举。
2. **可靠性与会话**：先完成 REQ-06 公共资源层，再联合验收 REQ-07 预算与 REQ-05 终端撤销。REQ-05 的数据库/JWT/HTTP 部分可并行，终端完整回收验收必须在 REQ-06/07 之后；REQ-08 部署逻辑也依赖这套传输契约。
3. **采集与图表**：REQ-08 → REQ-04；REQ-08 统一迁移部署调用并交付多架构镜像、指标与保留策略。图表 UI/可访问性可基于固定 mock 契约并行，但真实查询/峰值/coverage 验收在 REQ-08 之后。
4. **终端体验**：REQ-09，与连接/撤销验收同步；不扩展成多会话管理平台。

开工门禁：用户引用的 WP-2.2（执行/Relay 预算工作）以 §7.1.1–3 的固定值、TC-07-02/03/04 为契约；WP-3.1（监控调度工作）以 §8.1.1、TC-08-01 为契约。实施前将有效配置/可控时钟/RNG/断言登记到 coverage.json；不能带着候选数值开工或以“未实测”为由省略功能边界断言。性能目标仍需独立测量。

每项交付包含业务行为、相应测试、文档和必要升级说明；UI 与 API 描述一致。所有选中的必需验证通过才标记完成。新增数据库模型和 Timescale 策略应在隔离环境验证新库及历史升级路径，不以当前禁用外键的仓储测试代替完整迁移验证。

仅文档修改时核对链接、用例/需求映射和范围，不执行远程操作或生产迁移。本次完成的是架构修订与需求整理；全部业务实施、适配器删除和测试入口创建留待后续开发。

## 12. REQ-05/06/07/08 统一可观测性与预算验收

统一复用现有 `request_id`、`operation_id` 和 action 词汇，补充日志 schema 与进程内指标注册表。现有 `server.exec`、`server.terminal`、`service.relay` 名称保留；新增 action 固定为 `auth.jwt_check`、`auth.password_change`、`ssh.upload`、`agent.deploy`、`monitor.collect`、`metrics.aggregate`、`metrics.retention`，不把自由命令/URL 当 action。字段、指标和告警均为待实施契约，可先由测试快照读取，不要求新增外部监控平台。

### 12.1 结构化日志字段

| 字段组 | 约定 |
| --- | --- |
| 公共字段 | `schema_version=1`、UTC `timestamp`、`level`、`event`、`component`、`action`、`stage`、`outcome`、`reason_class`、`reason_code`、`duration_ms`、`budget_ms` |
| 关联字段 | 每个逻辑操作有 `operation_id`；HTTP 继承 `request_id`，子操作带 `parent_operation_id`；后台采集带 `collection_id`，不伪造 HTTP request ID |
| 适用身份/资源字段 | 日志中按需记录 `user_id`、`auth_type`、`server_id`、`session_id`、`generation`、`agent_arch`、`agent_version`、`agent_hash`、`attempt`、`bytes`、`scheduled_at`、`sample_at`、`schedule_lag_ms`、`queue_wait_ms`、`teardown_grace_ms` |
| 结果补充 | `forced_close`、`response_write_failed`、`cleanup_result`；改密码发起操作与被撤销 session 的关闭事件通过 operation/session ID 关联 |

`component` 只允许 auth/ssh/terminal/agent/monitor/metrics/relay；`stage` 使用 version_check/register/ready/revoke/quota_wait/dial/arch_probe/artifact_select/remote_inspect/upload/hash_verify/activate/run/write/store/cleanup/aggregate/retention。`outcome` 使用 success/failure/timeout/canceled/skipped/rejected/unknown。已有 Usage 语义映射为 succeeded→success、failed→failure、cancelled→canceled、rejected/unknown 保留；确认 deadline 到期时另分类 timeout，调度未开始使用 skipped，不反向改写 Usage 原始结果。清理失败或响应写入失败不能覆盖已提交的业务结果。

`reason_class` 使用 none/auth/host_key/quota/dial/probe/platform/architecture/artifact/integrity/permission/protocol/db/write/remote_exit/timeout/canceled/revoked/retention/other。`reason_code` 保存固定错误码，并保留资源层原始分类；超时/取消额外标明发生 stage。脱敏诊断文本有长度上限，不记录 JWT、API Key、密码、命令/输出、终端帧或完整请求体。每个逻辑操作最终事件恰好一次；重试使用 attempt/子事件，清理错误单独记录。

### 12.2 指标名称、单位与标签

| 指标 | 类型/单位 | 允许标签 |
| --- | --- | --- |
| `talus_operations_total` | Counter，逻辑操作次数 | action、outcome、reason_class |
| `talus_stage_duration_seconds` | Histogram，秒；包含配额等待、拨号、上传、运行与清理 | action、stage |
| `talus_auth_checks_total` | Counter，版本准入次数 | channel=http/ws/register/ready、outcome、reason_class |
| `talus_session_revocations_total` | Counter，被撤销 JWT 会话数 | outcome |
| `talus_ssh_leases_in_use` | Gauge，当前已取得配额的借用数 | consumer=exec/terminal/agent |
| `talus_workers_in_flight` | Gauge，在途 worker 数 | component |
| `talus_teardowns_total` | Counter，清理结果次数 | action、result=clean/forced/budget_exceeded |
| `talus_monitor_collections_total` | Counter，采集尝试及跳过数 | outcome、reason_class |
| `talus_monitor_schedule_lag_seconds` | Histogram，计划到开始的延迟，秒 | 无 |
| `talus_agent_updates_total` | Counter，部署尝试结果 | arch=amd64/arm64/unsupported、outcome、reason_class |

标签只能使用上述固定枚举，未知原因归 other。request/operation/user/server/session ID、主机、路径、命令、版本/hash、预算数值均只在日志/观测值中，不进入指标标签。原始监控样本、日志计数与操作计数不能混为一类；指标有明确的一次计数点，Gauge 的增减对应唯一资源 owner，避免多层重复统计或负数。

lease Gauge 唯一由 SSH 资源层在成功占用配额时 +1，归还/丢弃时幂等 -1；等待不计入，占用后拨号失败也须减回。Monitor 经 AgentDeploymentService 发起的整条链路统一 consumer=agent，探测/上传/校验/执行不重复增加，Monitor 与部署服务不直接更新该 Gauge。采集统计使用 monitor 的独立指标，不能再给同一 lease 标记 monitor。

### 12.3 告警与测试对齐

- 超过清理总预算产生独立 ERROR 清理事件及 `budget_exceeded`，并留下未回收资源状态；不覆盖业务最终结果或重复其计数，不会因为返回响应就计作清理成功。
- 同节点连续 3 次采集失败、或连续 3 次调度延迟超过有效采集周期/因防重入跳过，产生 WARN；下一次有效成功产生恢复事件。节点身份用于日志关联，不加入指标标签。
- 聚合/retention 一次失败即 WARN；未完成聚合时暂停相关删除。健康策略启用后原始数据最老年龄超过 37 天产生清理滞后事件，结合失败状态区分正常 chunk 余量与任务故障。
- 统一用可控 SSH/HTTP/DB 对端阻塞各阶段：断言有效 deadline、原始失败分类、最终事件和 Counter 恰好一次、日志关联完整、Gauge 在声明的清理期限内回到用例基线。覆盖成功/错误/超时/取消/漏采，不把应用其他正常会话的资源当泄漏。
- TC-05-04/05、TC-06-03、TC-07-04、TC-08-07 分别验证对应行为与共同字段；有效预算固定在用例环境中，断言使用单调时钟和声明的调度容差。日志断言检查敏感字段缺失，指标断言检查标签集合有界。读屏等人工用例单独标记，不计为自动化通过。

## 13. 本次缺口与文档落点

| 用户指出的缺口 | 明确后的落点 |
| --- | --- |
| 7. 上传前识别架构及失败分类 | §8.2.1；TC-08-04 |
| 8. 多架构产物如何进入 Hub 镜像 | §8.2.2；TC-08-05 |
| 9. 上传/部署唯一 owner | §6.1、§8.2.3；TC-06-03 |
| 10. JWT 终端回收交付依赖 | §5.4、§11：完整验收依赖 REQ-06/07 |
| 11. 注册与撤销线性化点 | §5.2；TC-05-04 |
| 12. 版本缓存与延迟预算 | §5.3；TC-05-05 |
| 13. 保留数值与容量估算 | §8.3.1/2；TC-08-06 |
| 14. 图表可访问性 | §4.5；TC-04-04 |
| 15. 统一日志、指标及告警 | §12；相关遥测用例 |
| 16. 适配器静态检查可执行化 | §2.3；TC-01-01/03 |
