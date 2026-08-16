# Talus

> 你的服务器舰队的青铜守卫。在一个地方完成监控、命令执行和远程连接。

**Talus** 是一个自托管的单用户平台，用于管理少量 Linux 服务器。无需安装 Agent，无需开放额外端口——只需要 SSH 和一个 Web 控制台。

## 功能

- **服务器清单** — 按名称、IP 和 SSH 端口注册你的 VPS 和裸金属主机。
- **凭证保险库** — 使用 AES-256-GCM 加密存储 SSH 密码和私钥。
- **远程 Shell** — 在任意服务器上执行命令，查看 stdout、stderr 和退出码。
- **交互式终端** — 在浏览器中打开完整的 PTY 会话（xterm.js，支持窗口缩放，基于 WebSocket）。
- **实时监控** — CPU、内存、磁盘、负载、Swap、网络和磁盘 I/O 图表，支持 1 小时 / 6 小时 / 24 小时 / 7 天时间范围。临时 Agent 通过 SSH 按需部署。
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
- **临时 Agent**：监控数据由 Hub 通过 SSH 部署并运行一个静态编译的 Go 二进制文件来采集——无守护进程、无开放端口、无残留文件。
- **凭证加密**：使用你掌控的主密钥进行静态加密存储，API 响应中永不返回。

## 快速开始

### 前置条件

- Docker 和 Docker Compose
- 可通过 SSH 访问的 Linux 服务器（推荐 Debian/Ubuntu）

### 1. 克隆仓库

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
```

### 2. 配置（可选）

所有密钥都有开发默认值，开箱即用。除本地测试外，请复制 `.env.example` 为 `.env` 并设置真实值：

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

### 4. 添加服务器

1. 进入 **Servers**（服务器）→ **Add Server**（添加服务器）
2. 填写名称、主机（IP）、SSH 端口和描述
3. 进入 **Credentials**（凭证）→ **Add Credential**（添加凭证），关联密码或私钥
4. 服务器上线——可以执行命令、打开终端或查看监控指标。

### 5. 代理外部服务

1. 进入 **Services**（服务）→ **Add Service**（添加服务）
2. 填写名称、显示名称、基础 URL（如 `http://localhost:3000`）和凭据（键值对）
3. 可选：将服务关联到某台服务器，通过 SSH 隧道访问
4. 使用 relay API 通过 Talus 代理请求——凭据自动注入，`{{key}}` 占位符自动替换

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

### 安装服务目录注入插件（推荐）

[ai-integration](../ai-integration/) 插件（OpenCode / pi / Claude Code / Codex）
在**用户提到服务时**注入服务目录——让 AI 在需要时恰好看到有哪些服务可用，并被提醒
读取使用指南，同时无关对话轮次不消耗 token。注入只影响当前这一轮，不会写入对话历史。
在安装 skill 之后安装它：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/molicherry/Talus/main/ai-integration/install.sh)
```

没有提示词注入钩子的平台（如 Cursor）依赖 skill 的发现规则——两种方式能力都完整；
插件只是让它不可能被漏掉。

### 示例提示词

```bash
"通过 Talus 列出所有服务器"
"查看 web-01 的 CPU 指标"
"在 prod-db 上执行 docker ps"
"向 Grafana 服务转发请求"
```

## 配置项

| 变量 | 默认值 | 描述 |
|------|--------|------|
| `DB_PASSWORD` | *(必填)* | PostgreSQL 数据库密码 |
| `VPSMANAGER_MASTER_KEY` | *(必填)* | SSH 凭证加密的 64 位十六进制主密钥 |
| `JWT_SECRET` | *(必填)* | JWT 签名密钥 |
| `PORT` | `8080` | HTTP 服务端口 |
| `LOG_LEVEL` | `info` | 日志级别：`debug`、`info`、`warn`、`error` |
| `MONITOR_INTERVAL` | `60` | 指标采集间隔（秒） |
| `SSH_TIMEOUT` | `10` | SSH 连接超时（秒） |
| `EXEC_TIMEOUT` | `30` | 命令执行超时（秒） |

## 安全性

- SSH 私钥和密码在存储前使用 **AES-256-GCM** 加密，密钥通过 Argon2id 从 `VPSMANAGER_MASTER_KEY` 派生。
- **API 响应中永不返回**凭证内容。
- 目标服务器**无需开放额外端口**——监控数据通过现有的 SSH 通道传输。
- 监控 Agent 是**临时的**：按需部署，采集指标后退出。不会在目标服务器上留下持久化二进制文件或守护进程。
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
      MONITOR_INTERVAL: ${MONITOR_INTERVAL:-60}
      SSH_TIMEOUT: ${SSH_TIMEOUT:-10}
      EXEC_TIMEOUT: ${EXEC_TIMEOUT:-30}
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

## 许可证

[MIT](../LICENSE)
