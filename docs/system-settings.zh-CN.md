# 系统参数与配置位置

系统设置不是部署配置的替代品：同一参数可以在部署时提供初始值，也可以由管理员进入项目后覆盖。以下清单覆盖 Hub 运行参数、Compose 补充项和前端构建参数；测试工具专用环境变量不属于产品配置。

## 保存、回退与生效

入口：管理员登录后，侧栏 **系统设置**（`/settings`）。优先级：**数据库中的后台保存值 > Hub 启动时的环境变量 > 程序默认值**。

- 每项显示来源、部署回退值和生效方式。修改后点击页面底部“保存设置”。
- “恢复部署配置”再保存会删除该项后台覆盖。部署环境未提供该项时恢复程序默认值。
- 明确保存的空字符串和 `false` 都是有效覆盖，不等于恢复部署配置。
- 修改 `.env` 后需执行 `docker compose up -d hub` 重建 Hub 容器；`docker compose restart hub` 不会重新读取 `.env`。新环境值不会覆盖数据库中已有的设置。
- 后台保存的容量参数需 `docker compose restart hub`；页面同时展示保存值、当前生效值和待重启提示。
- 配置写入和审计记录在同一个数据库事务中完成；保存失败不会改变运行配置。多人修改同一版本时后提交者收到冲突，页面保留草稿，重新加载会丢弃草稿。
- 仅管理员 JWT 登录会话可访问设置 API。普通用户、API Key（包括通配权限）不可读写这些设置。
- 多实例每 5 秒按 `revision` 检查更新；数据库读取或校验失败时保留上一次有效配置，首次失败及持续失败每分钟记录告警，恢复后记录恢复日志。仅修改 `overrides` 而不递增 `revision` 不会触发刷新。正常修改请使用设置 API；直接修复数据库必须在同一事务中更新覆盖文档和递增版本，并防止覆盖并发写入。

## 可以在后台和部署环境中配置的 19 项

“高级”表示该分组内的高级选项。下面的范围是后台允许保存的范围。部署值沿用原有解析和兼容行为；建议使用相同有效范围。

| 参数 | 默认值 / 后台范围 | 配置区域 | 保存后何时生效 |
| --- | --- | --- | --- |
| `AGENT_HUB_URL` | 空；节点可访问的 Hub HTTPS 地址，不含账号、密码、查询参数或片段 | 监控 | 安排 Agent 配置同步，以各节点状态为准 |
| `MONITOR_INTERVAL` | `5`；1–60 秒 | 监控 | Agent 同步后采用新采样间隔 |
| `AGENT_ALLOW_HTTP` | `false` | 监控 → 高级 | Agent 同步；仅用于可信内网或测试 HTTP |
| `SSH_TIMEOUT` | `10`；1–300 秒 | SSH 连接 | 新连接；连接测试总预算仍不超过 10 秒 |
| `SSH_MAX_IDLE` | `300`；1–86400 秒 | SSH 连接 → 高级 | 下一次空闲连接回收，不中断活动 lease |
| `EXEC_TIMEOUT` | `30`；1–300 秒，且不大于 `EXEC_TIMEOUT_MAX` | 命令执行 | 新命令默认总预算，包括建连和执行 |
| `EXEC_TIMEOUT_MAX` | `300`；1–300 秒，且不小于 `EXEC_TIMEOUT` | 命令执行 → 高级 | 新命令请求超时的上限 |
| `EXEC_OUTPUT_LIMIT` | `8388608`；1–8388608 字节 | 命令执行 → 高级 | 新操作保留的 stdout + stderr 总上限 |
| `EXEC_OUTPUT_STREAM_LIMIT` | `4194304`；1–4194304 字节，且不大于总上限 | 命令执行 → 高级 | 新操作单个输出流的保留上限 |
| `USAGE_LOG_RETENTION_DAYS` | `30`；1–36500 天 | 数据保留 | 后续记录写入与清理周期；缩短需确认 |
| `USAGE_LOG_SENSITIVE_RETENTION_DAYS` | `90`；1–36500 天 | 数据保留 | 敏感操作、删除及系统设置变更的日志保留；缩短需确认 |
| `METRICS_RETENTION_ENABLED` | `false` | 数据保留 → 高级 | 下一清理周期；仅开启此项还不会实际删除 |
| `METRICS_RETENTION_DRY_RUN` | `true` | 数据保留 → 高级 | 仅当 enabled 为 true 且 dry-run 为 false 时实际删除，启用删除需确认 |
| `LOGIN_RATE_LIMIT` | `10`；1–10000 次/IP/分钟 | 登录保护 | 下一次登录请求；保留当前时间窗内的计数 |
| `LOG_LEVEL` | `info`；debug / info / warn / error | 应用日志 | 立即 |
| `LOG_FORMAT` | `json`；json / text | 应用日志 → 高级 | 立即，控制应用结构化日志格式 |
| `DB_MAX_OPEN_CONNECTIONS` | `32`；12–10000 | 容量（高级） | **重启 Hub** |
| `USAGE_LOG_WRITE_CONCURRENCY` | `4`；1–10000 | 容量（高级） | **重启 Hub**；实际日志并发还受数据库连接预算约束 |
| `USAGE_LOG_ACTIVE_LIMIT` | `4096`；1–1000000 | 容量（高级） | **重启 Hub** |

`DB_MAX_OPEN_CONNECTIONS` 是一个 Hub 进程最多同时打开的 PostgreSQL 连接数，不是最大在线用户数。连接都被占用时，新数据库操作需要等待；设置过大则会占用数据库资源。它还参与使用日志写入、协调与清理的连接预算，所以保存后与日志容量项一起在重启时应用。多进程部署要考虑所有进程连接数之和以及 PostgreSQL 自身的连接上限。

`USAGE_LOG_ACTIVE_LIMIT` 限制内存里同时跟踪的使用日志操作数量，不是服务器数量。`USAGE_LOG_WRITE_CONCURRENCY` 限制日志写入并发，程序会为其他业务和维护保留数据库连接。

已有 SSH 操作使用操作开始时捕获的快照；修改系统设置不会重设其超时或输出预算。命令面板另有可选的本次超时，留空使用全局默认，请求仍受全局上限约束。

Agent 地址保存成功只说明配置已持久化，不代表目标节点可达。系统设置下方展示各节点的部署、连接、采集和数据状态。地址或采样配置改变会触发同步；正在进行的旧安装先取消并回滚，再按新快照重试。清空地址会停止自动安装/更新，不会主动卸载已有 Agent 或删除历史指标。

指标保留开关控制现有的合格原始/聚合指标清理策略，默认演练，超过 35 天且满足聚合条件的数据才可删除；页面不提供修改该固定保留窗口的功能。使用日志保留天数作用于后续清理，延长保留期不能恢复已删除的数据。

使用日志的 `retention_at` 是**保留期起算时间，不是到期时间**：正常操作使用完成时间，崩溃恢复记录使用协调完成时间，历史审计摘要使用原事件时间。清理在起算时间加保留天数后进行，不会重复计算两次；运行中操作不清理。`settings.update` 的使用日志摘要至少保留 90 天，敏感日志策略更长时采用更长值。完整设置变更详情另在 `audit_events` 中事务性保存，不由使用日志清理器删除。

## 只在部署时配置的项目

| 参数 | 默认值 | 在哪里配置 / 用途 |
| --- | --- | --- |
| `DATABASE_URL` | 必填 | Hub 环境：数据库连接串。仓库 Compose 根据 `DB_PASSWORD` 自动生成 |
| `VPSMANAGER_MASTER_KEY` | 必填 | Hub 环境/密钥管理：凭据加密主密钥。数据库迁移、恢复时保持原密钥 |
| `JWT_SECRET` | 必填 | Hub 环境/密钥管理：JWT 签名及相关会话凭据 |
| `PORT` | `8080` | 独立进程为监听端口；仓库 Compose 的 `.env` 值控制宿主机端口映射，Hub 容器内部仍为 8080 |
| `AGENT_ARTIFACTS_DIR` | `/usr/local/bin` | Hub 环境与挂载：Agent manifest 及 Linux amd64/arm64、Windows amd64 程序目录；三个平台均须提供，启动校验 |
| `TRUST_PROXY` | `false` | Hub 环境：仅在可信反向代理覆盖 X-Forwarded-For 时开启，影响登录 IP 识别 |
| `STATIC_DIR` | 独立进程为空；镜像 `/app/static` | Hub 环境与挂载：前端静态资源目录 |
| `DB_PASSWORD` | 必填 | Compose `.env`：PostgreSQL 初始化密码及 Hub 连接串；不是 Hub 独立读取的参数 |
| `USAGE_LOG_LEGACY_WRITERS_DRAINED` | `false` | 升级运维声明，说明旧版实例及其事务已停止；下文说明 |
| `SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES` | `false` | 严格布尔值；仅用于非法数据库覆盖导致启动失败时的恢复，下文说明 |
| `MONITOR_TOLERATED_ROUNDS` | `1` | 已弃用的兼容环境项；不参与常驻 Agent 的联系超时判定 |
| `MONITOR_ALLOWED_LATENCY_SECONDS` | `0` | 已弃用的兼容环境项；不参与常驻 Agent 的数据新鲜度判定 |

仓库 Compose 和中英文生产部署示例传入全部 19 项后台参数，以及 `TRUST_PROXY`、迁移开关和必需密钥。自定义的目录、数据库或旧兼容项需直接配置进程环境，或明确加入 Compose 的 `environment` 并提供相应挂载。仅把未映射变量写进 `.env` 不会传入容器。

常驻监控的联系超时为 45 秒；数据新鲜度为三个采样周期，最少 15 秒。两个旧监控兼容参数保留读取以兼容旧部署，不在设置页继续提供入口。

### `USAGE_LOG_LEGACY_WRITERS_DRAINED` 的含义

它是历史审计日志回填的运维前置声明：**所有旧版 Hub 写入者及其未完成事务已经停止**。程序无法自行证明这一点，所以不把它做成后台普通开关。

升级时先停止旧实例并等待其事务结束，再由运维在 Hub 环境中设置为 `true` 并重建容器，允许执行可恢复的历史回填。系统设置页只显示等待声明、待完成、已完成或状态读取失败。确认回填完成后可移除该变量。不要为了消除提示就在旧实例仍写入时设置为 true。新部署没有历史日志需要恢复时无需操作该开关。

### 启动校验与非法覆盖恢复

启动时先校验无法由后台覆盖修复的部署字段、Agent 产物和主密钥，再连接数据库、准备 schema 并迁移；随后加载数据库覆盖，校验合并后的完整配置，通过后才启动 HTTP 和后台服务。`EXEC_TIMEOUT_MAX < EXEC_TIMEOUT` 等跨字段错误可以由数据库覆盖修复，因此也必须在合并后判断。无法修复的最终配置会在 schema 准备之后停止启动。

非法数据库覆盖默认阻止启动；日志包含出错 `revision` 和 `key`，不输出覆盖值。需要恢复时：

1. 确认部署环境本身能组成有效配置，在 Compose `.env` 设置 `SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES=true`，执行 `docker compose up -d hub` 重建。
2. 只有覆盖文档校验失败时，Hub 才忽略**整份覆盖文档**并采用部署值；原数据库文档保持不变。数据库不可访问或部署配置本身无效时仍会停止启动。合法覆盖仍照常生效。
3. 登录系统设置，页面会显示恢复提示和错误键。检查全部配置后修改并保存；保存会按原 revision 检查冲突、替换被忽略的文档并记录审计，而不是只修复某一个非法键。
4. 修复后移除恢复开关并重建 Hub；有容量变更时以重启后的生效值为准。多实例须逐个检查，恢复期间应暂停其他实例的配置写入。

恢复开关仅处理启动时的非法覆盖。运行中的新版本校验失败时，实例继续保留上次有效快照并记录告警，不会突然切回部署值。

## 构建与镜像选择

这些配置不属于运行中的系统设置，修改需重新构建或部署对应产物。

| 参数 | 位置 / 用途 |
| --- | --- |
| `VERSION` | Docker 构建参数；仓库 Compose 默认 `dev`，写入 Hub/Agent/UI 版本 |
| `TALUS_VERSION` | README 生产 Compose 的 `.env`：选择 GHCR 镜像标签，默认 `latest` |
| `GOPROXY` | Docker 构建参数：Go 模块下载代理 |
| `NPM_REGISTRY` | Docker 构建参数：npm 包下载源 |
| `TARGETOS`、`TARGETARCH` | Docker BuildKit 提供的目标操作系统/架构 |
| `VITE_APP_VERSION` | 前端构建环境：版本展示及更新检查，Docker 构建从 `VERSION` 注入 |
| `VITE_API_BASE_URL` | 前端构建环境：前后端分离时的 Hub API 地址；默认同源，不是节点使用的 `AGENT_HUB_URL` |

## API 约定

`GET /api/v1/settings` 返回版本 `revision`、19 项白名单设置及只读迁移状态。每项有 `value`、`effective_value`、`fallback_value`、`source`、`fallback_source`、`restart_required`；来源为 `database` / `environment` / `default`。容量项的 effective 值对应当前 Hub 进程，监控项的 effective 值表示 Hub 已接受的配置，节点应用进度需看监控状态。

`PUT /api/v1/settings` 接收稀疏修改；未出现的项不变。需要管理员的 `Authorization: Bearer <JWT>`。

```json
{
  "revision": 1,
  "values": {
    "AGENT_HUB_URL": "https://hub.example.com",
    "MONITOR_INTERVAL": 10,
    "LOG_LEVEL": null
  },
  "confirm_retention_change": false
}
```

`null` 删除指定后台覆盖。版本冲突返回 409；缩短保留期/启用实际删除且未确认返回 409 `settings_retention_confirmation`；未知或非法参数返回 422 `settings_invalid`，能够定位字段时在 `error.params.key` 返回键名；未授权/非管理员返回 401/403。保存成功返回新的版本和配置。恢复启动的响应额外包含 `recovery: {revision, key}`。使用日志中可按“系统设置变更”筛选；变更前后值和覆盖状态保存在审计记录中，不记录部署密钥。
