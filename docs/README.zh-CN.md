# Talus

> 你的服务器舰队的青铜守卫。在一个地方完成监控、命令执行和远程连接。

**Talus** 是一个自托管的单用户平台，用于管理少量 Linux 服务器。无需安装 Agent，无需开放额外端口——只需要 SSH 和一个 Web 控制台。

## 功能

- **服务器清单** — 按名称、IP 和 SSH 端口注册你的 VPS 和裸金属主机。
- **凭证保险库** — 使用 AES-256-GCM 加密存储 SSH 密码和私钥。
- **远程 Shell** — 在任意服务器上执行命令，查看 stdout、stderr 和退出码。
- **交互式终端** — 在浏览器中打开完整的 PTY 会话（xterm.js，支持窗口缩放，基于 WebSocket）。
- **实时监控** — CPU、内存、磁盘、负载、Swap、网络和磁盘 I/O 图表，支持 15 分钟 / 1 小时 / 6 小时 / 24 小时 / 7 天 / 30 天时间范围、平均值与峰值视图及可访问的数据表格。Hub 通过 SSH 部署适配目标架构的监控 Agent。
- **API 密钥** — 创建带作用域和服务器级别访问控制的 API 密钥。二维权限模型（操作 × 服务器）。密钥加密存储，支持按需复制，带有审计日志和速率限制。
- **服务代理** — 注册外部服务（Grafana、Portainer 等），加密存储凭据。通过 Talus 代理 API 请求，支持凭据注入和占位符替换。编辑时可加载已有凭据，支持显示/隐藏切换和复制。

## 架构

```
  浏览器                   Talus Hub                    目标服务器
┌──────────┐   HTTP/WS    ┌──────────────┐    SSH      ┌──────────┐
│  React   │ ◄──────────► │  Go 后端     │ ◄─────────► │  Linux   │
│  SPA     │              │  (chi, GORM) │             │  服务器  │
└──────────┘              │              │             └──────────┘
                          │  PostgreSQL  │
                          │  + Timescale │
                          └──────────────┘
```

- **中心辐射架构**：Hub 通过 SSH 连接到你的服务器，服务器无需回连。
- **按需运行 Agent**：监控数据由 Hub 通过 SSH 部署并运行一个静态编译的 Go 二进制文件来采集。采集后进程退出，已校验的二进制文件保留在 `/tmp/vpsmanager-agent` 供后续复用，无需守护进程或额外开放端口。
- **凭证加密**：使用你掌控的主密钥进行静态加密存储。列表响应不返回秘密值，已认证的管理员可通过专用接口查看，并记录审计日志。

## 快速开始

### 前置条件

- Docker 和 Docker Compose
- 可通过 SSH 访问的 Linux 服务器（推荐 Debian/Ubuntu）

### 1. 克隆仓库

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
```

### 2. 配置

复制 `.env.example` 为 `.env` 并设置真实值——未设置时服务无法启动：

```env
DB_PASSWORD=<你的数据库密码>
VPSMANAGER_MASTER_KEY=<使用 openssl rand -hex 32 生成>
JWT_SECRET=<使用 openssl rand -hex 32 生成>
```

### 3. 启动（源码构建）

仓库内的 `docker-compose.yml` 从源码构建 Talus：

```bash
docker compose up -d --build
```

访问控制台：**http://localhost:8080**

首次登录时，输入任意用户名和至少 8 位的密码，系统会自动创建管理员账户。

### 4. 添加服务器

1. 进入 **Servers**（服务器）→ **Add Server**（添加服务器）
2. 填写名称、主机（IP）、SSH 端口和描述
3. 进入 **Credentials**（凭证）→ **Add Credential**（添加凭证），关联密码或私钥
4. 服务器上线——可以执行命令、打开终端或查看监控指标。

### 5. 代理外部服务

1. 进入 **Services**（服务）→ **Add Service**（添加服务）
2. 填写名称、显示名称、基础 URL（如 `http://localhost:3000`）和凭据（键值对）
3. 可选：将服务关联到某台服务器，用于管理和授权范围；Hub 直接请求服务的基础 URL，不使用 SSH 隧道
4. 使用 relay API 通过 Talus 代理请求——凭据自动注入，`{{key}}` 占位符自动替换。不传 `mode` 时总预算为 30 秒；显式传入 `"mode": "bounded_stream"` 可启用最长 300 秒的流式请求，响应头等待和无进展空闲上限均为 30 秒，`mode` 不转发给上游

### 6. 查看使用日志

管理员可进入 **使用日志**，筛选操作历史并查看详情。日志覆盖资源变更、敏感信息查看、SSH 命令执行、服务转发和终端会话，不记录密钥等秘密信息、命令内容或流式传输内容。API 密钥无法查询这些日志。

## 技术栈

| 层级 | 技术 |
|------|------|
| 后端 | Go、[chi](https://github.com/go-chi/chi)、[GORM](https://gorm.io)、[gorilla/websocket](https://github.com/gorilla/websocket)、[golang-jwt](https://github.com/golang-jwt/jwt) |
| 前端 | React 19、TypeScript、[Vite](https://vite.dev)、[Tailwind CSS](https://tailwindcss.com)、[xterm.js](https://xtermjs.org) |
| 数据库 | PostgreSQL 16 + [TimescaleDB](https://www.timescale.com) |
| 监控 Agent | Go + [gopsutil](https://github.com/shirou/gopsutil) |
| 部署 | Docker Compose |

## AI 集成

Talus 附带一个**可移植的** [agent skill](../skills/talus/SKILL.md)，任何 AI 编程助手
（OpenCode、Claude Code、Cursor 等）都可以通过 Talus REST API 操作它：
列出服务器、执行命令、查询指标、向已注册服务转发请求、添加/更新服务器。

### 准备

- 一个运行中的 Talus 实例（见上文部署）
- 一个 API 密钥——在 **Talus Web UI → API Keys** 创建（默认作用域覆盖读/执行/指标；
  `servers:write` 和 `services:relay` 为可选勾选）

### 安装

把 skill 复制到你所用 AI 工具的 skills 目录（skill 是可移植的，不要求放在本仓库内）：

```bash
# OpenCode
mkdir -p ~/.config/opencode/skills && cp -r skills/talus ~/.config/opencode/skills/
# Claude Code
mkdir -p ~/.claude/skills && cp -r skills/talus ~/.claude/skills/
```

把助手指向你的实例：

```bash
export TALUS_URL=https://your-talus.example.com   # 默认 http://localhost:8080
export TALUS_API_KEY=<你的 API 密钥>
```

### 验证

在新的 AI 会话中问 *"通过 Talus 列出所有服务器"*——返回服务器列表（或空数组）即表示 skill 已生效。

### 服务：已注册服务优先走 Talus relay

已注册服务（Dokploy、Portainer 或任意内部应用）由 Talus 代理——它们的 `base_url`
是 AI 无法直接访问的内网地址，凭据在 relay 时由 Talus 注入。在服务器上操作某个服务时，
AI 应首先检查它是否已在 Talus 注册（`GET /api/v1/services?server_id=<id>`）；若已注册，
通过 relay 使用它；若未注册，AI 可以直接在服务器上操作（并可考虑补注册）。已注册服务的
工作流：

```
GET /api/v1/services             → 服务目录（name、description、指南摘要）
GET /api/v1/services/{id}        → 完整 usage_guide（每个服务自己的 "skill"）
POST /api/v1/services/{id}/relay → 按指南构造请求
```

每个已注册服务都可以携带自己的**使用指南**（`usage_guide`，markdown）——在
**Talus Web UI → 服务 → 添加/编辑服务 → 使用指南** 字段中编写。它告诉 AI 如何使用该服务
（端点、认证头、请求示例），而无需把任何服务细节硬编码进通用 skill。


### 示例提示词

```bash
"通过 Talus 列出所有服务器"
"查看 web-01 的 CPU 指标"
"在 prod-db 上执行 docker ps"
"向 Grafana 服务转发请求"
```

## 配置项

这些变量需配置到 Hub 进程或容器的环境中。使用 Compose 时，未列出的变量须补到 Hub 的 `environment` 中；仅写入 `.env` 不会自动传入容器。

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `DB_PASSWORD` | *(必填)* | PostgreSQL 数据库密码 |
| `VPSMANAGER_MASTER_KEY` | *(必填)* | SSH 凭证加密的 64 位十六进制主密钥 |
| `JWT_SECRET` | *(必填)* | JWT 签名密钥 |
| `PORT` | `8080` | HTTP 服务端口 |
| `LOG_LEVEL` | `info` | 日志级别：`debug`、`info`、`warn`、`error` |
| `DB_MAX_OPEN_CONNECTIONS` | `32` | 数据库最大连接数，最小为 `12` |
| `USAGE_LOG_WRITE_CONCURRENCY` | `4` | 使用日志写入并发上限 |
| `USAGE_LOG_ACTIVE_LIMIT` | `4096` | 内存中同时跟踪的活动操作数量上限 |
| `USAGE_LOG_RETENTION_DAYS` | `30` | 普通使用日志保留天数 |
| `USAGE_LOG_SENSITIVE_RETENTION_DAYS` | `90` | 敏感操作和删除操作日志保留天数 |
| `USAGE_LOG_LEGACY_WRITERS_DRAINED` | `false` | 仅在所有旧实例及其事务停止后设为 `true`，启用历史审计日志回填；确认回填完成后可移除 |
| `MONITOR_INTERVAL` | `60` | 指标采集间隔（秒），通过 Hub 环境变量设置 |
| `MONITOR_TOLERATED_ROUNDS` | `1` | 节点状态允许漏采的轮数，须为非负整数 |
| `MONITOR_ALLOWED_LATENCY_SECONDS` | `0` | 额外允许的数据延迟（秒），须为非负整数 |
| `AGENT_ARTIFACTS_DIR` | `/usr/local/bin` | 包含 `agents-manifest.json` 及 Linux amd64/arm64 双架构 Agent 的目录，启动时校验 |
| `METRICS_RETENTION_ENABLED` | `false` | 启用超过 35 天的合格原始指标及聚合指标清理，实际删除还须关闭演练模式 |
| `METRICS_RETENTION_DRY_RUN` | `true` | 仅报告清理计划；只有 `METRICS_RETENTION_ENABLED=true` 且本值为 `false` 才会删除 |
| `SSH_TIMEOUT` | `10` | SSH 连接超时（秒） |
| `EXEC_TIMEOUT` | `30` | 默认命令总预算（秒），包含 SSH 建连和执行；请求可覆盖，但不超过配置上限（默认 300 秒） |
| `EXEC_TIMEOUT_MAX` | `300` | 命令总预算上限（秒），须不小于 `EXEC_TIMEOUT` 且不超过 `300` |
| `EXEC_OUTPUT_LIMIT` | `8388608` | 保留的 stdout 与 stderr 合计字节上限，须为正数且不超过 8 MiB |
| `EXEC_OUTPUT_STREAM_LIMIT` | `4194304` | 单个输出流保留的字节上限，须为正数、不超过 4 MiB，且不大于 `EXEC_OUTPUT_LIMIT` |

未收到新样本超过 `MONITOR_INTERVAL × (MONITOR_TOLERATED_ROUNDS + 1) + MONITOR_ALLOWED_LATENCY_SECONDS` 秒后，节点数据视为过期，默认阈值为 120 秒。

## 安全性

- SSH 私钥和密码、服务凭据及 API 密钥原文在存储前使用 **AES-256-GCM** 加密，密钥通过 Argon2id 从 `VPSMANAGER_MASTER_KEY` 派生。
- 列表响应不返回凭证或 API 密钥原文。专用查看接口仅允许 JWT 认证，按用户限流（每分钟 5 次）并记录审计日志。
- 目标服务器**无需开放额外端口**——监控数据通过现有的 SSH 通道传输。
- 监控 Agent **按需运行**，采集指标后进程退出。Hub 复用前校验保留的二进制文件，必要时原子替换，目标服务器无需运行 Agent 守护进程。
- 修改密码会使已有 JWT 失效，并主动关闭该用户的终端会话。API 密钥使用独立的生命周期。
- SSH 主机密钥采用 **TOFU（首次信任）** 机制验证：首次连接时记录密钥，后续连接验证匹配，防止中间人攻击。

## 生产部署（GHCR 镜像）

预构建镜像由 CI 在每个版本 tag（`v*`）发布到 [GHCR](https://github.com/molicherry/Talus/pkgs/container/talus)。
不想编译的话，把下面的 compose 保存为 `docker-compose.prod.yml`：

```yaml
# docker-compose.prod.yml — 使用预构建 GHCR 镜像部署 Talus
services:
  db:
    image: timescale/timescaledb:latest-pg16
    environment:
      POSTGRES_USER: vpsmanager
      POSTGRES_PASSWORD: ${DB_PASSWORD:?请在 .env 中设置 DB_PASSWORD}
      POSTGRES_DB: vpsmanager
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U vpsmanager"]
      interval: 5s
      timeout: 5s
      retries: 5
    restart: unless-stopped

  hub:
    image: ghcr.io/molicherry/talus:${TALUS_VERSION:-latest}
    ports:
      - "${PORT:-8080}:8080"
    environment:
      DATABASE_URL: postgres://vpsmanager:${DB_PASSWORD:?请在 .env 中设置 DB_PASSWORD}@db:5432/vpsmanager?sslmode=disable
      VPSMANAGER_MASTER_KEY: ${VPSMANAGER_MASTER_KEY:?请在 .env 中设置 VPSMANAGER_MASTER_KEY}
      JWT_SECRET: ${JWT_SECRET:?请在 .env 中设置 JWT_SECRET}
      PORT: 8080
      LOG_LEVEL: ${LOG_LEVEL:-info}
      DB_MAX_OPEN_CONNECTIONS: ${DB_MAX_OPEN_CONNECTIONS:-32}
      USAGE_LOG_WRITE_CONCURRENCY: ${USAGE_LOG_WRITE_CONCURRENCY:-4}
      USAGE_LOG_ACTIVE_LIMIT: ${USAGE_LOG_ACTIVE_LIMIT:-4096}
      USAGE_LOG_RETENTION_DAYS: ${USAGE_LOG_RETENTION_DAYS:-30}
      USAGE_LOG_SENSITIVE_RETENTION_DAYS: ${USAGE_LOG_SENSITIVE_RETENTION_DAYS:-90}
      USAGE_LOG_LEGACY_WRITERS_DRAINED: ${USAGE_LOG_LEGACY_WRITERS_DRAINED:-false}
      MONITOR_INTERVAL: ${MONITOR_INTERVAL:-60}
      SSH_TIMEOUT: ${SSH_TIMEOUT:-10}
      EXEC_TIMEOUT: ${EXEC_TIMEOUT:-30}
      EXEC_TIMEOUT_MAX: ${EXEC_TIMEOUT_MAX:-300}
      EXEC_OUTPUT_LIMIT: ${EXEC_OUTPUT_LIMIT:-8388608}
      EXEC_OUTPUT_STREAM_LIMIT: ${EXEC_OUTPUT_STREAM_LIMIT:-4194304}
      METRICS_RETENTION_ENABLED: ${METRICS_RETENTION_ENABLED:-false}
      METRICS_RETENTION_DRY_RUN: ${METRICS_RETENTION_DRY_RUN:-true}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

volumes:
  pgdata:
```

部署：

```bash
cp .env.example .env   # 填入 DB_PASSWORD / VPSMANAGER_MASTER_KEY / JWT_SECRET
docker compose -f docker-compose.prod.yml up -d
```

**升级到新版本：**

```bash
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

从尚未支持 JWT 撤销的版本首次升级时，须先停止全部旧 Hub 实例并结束旧 WebSocket 会话，再启动新版本，不能让两类版本并行服务。后续升级和回滚均须保留令牌版本校验与主动会话撤销。

### 反向代理超时

Talus 使用路由级写入预算：普通 HTTP API 为 15 秒，Exec 最长执行 300 秒，显式开启的 Relay 流式请求最长 300 秒。前置代理须额外预留结果返回及清理时间：

| 路由 | 代理超时下限 |
| --- | --- |
| `POST /api/v1/servers/*/exec`、`POST /api/v1/services/*/relay` | ≥ 330 秒 |
| 普通 HTTP API | ≥ 15 秒 |

`/api/v1/services/*/relay` 应关闭响应缓冲，以便增量转发流式内容。终端路由须启用 WebSocket 升级，并按预期交互会话时长配置空闲超时；上表中的普通 HTTP 超时不代表终端会话时限。

## 测试

通过[统一测试入口](../tests/README.md)运行 `fast`、`integration`、`ui`、`e2e` 套件及 M1–M4 门禁。[测试索引](../tests/coverage.md)由机器清单生成，列出场景、实现和执行命令。

## 许可证

[MIT](../LICENSE)
