# Talus

> 你的服务器舰队的青铜守卫。在一个地方完成监控、命令执行和远程连接。

**Talus** 是一个自托管的单用户平台，用于管理少量 Linux 服务器。无需安装 Agent，无需开放额外端口——只需要 SSH 和一个 Web 控制台。

## 功能

- **服务器清单** — 按名称、IP 和 SSH 端口注册你的 VPS 和裸金属主机。
- **凭证保险库** — 使用 AES-256-GCM 加密存储 SSH 密码和私钥。
- **远程 Shell** — 在任意服务器上执行命令，查看 stdout、stderr 和退出码。
- **交互式终端** — 在浏览器中打开完整的 PTY 会话（xterm.js，支持窗口缩放，基于 WebSocket）。
- **实时监控** — CPU、内存、磁盘、负载、Swap、网络和磁盘 I/O 图表，支持 1 小时 / 6 小时 / 24 小时 / 7 天时间范围。临时 Agent 通过 SSH 按需部署。
- **API 密钥** — 创建和管理 API 密钥，支持程序化访问。
- **国际化** — 英文和中文界面，支持亮色/暗色/跟随系统主题。
- **Docker Compose** — 一条命令启动：`docker compose up`。

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

### 2. 配置

```bash
cp .env.example .env
```

编辑 `.env` 并设置：

```env
DB_PASSWORD=<你的数据库密码>
VPSMANAGER_MASTER_KEY=<使用 openssl rand -hex 32 生成>
JWT_SECRET=<使用 openssl rand -hex 32 生成>
```

### 3. 启动

```bash
docker compose up -d
```

访问控制台：**http://localhost:8080**

首次登录时，输入任意用户名和密码即可——首次登录会自动创建管理员账号。

### 4. 添加服务器

1. 进入 **Servers**（服务器）→ **Add Server**（添加服务器）
2. 填写名称、主机（IP）、SSH 端口和描述
3. 进入 **Credentials**（凭证）→ **Add Credential**（添加凭证），关联密码或私钥
4. 服务器上线——可以执行命令、打开终端或查看监控指标。

## 技术栈

| 层级 | 技术 |
|------|------|
| 后端 | Go、[chi](https://github.com/go-chi/chi)、[GORM](https://gorm.io)、[gorilla/websocket](https://github.com/gorilla/websocket)、[golang-jwt](https://github.com/golang-jwt/jwt) |
| 前端 | React 19、TypeScript、[Vite](https://vite.dev)、[Tailwind CSS](https://tailwindcss.com)、[Tremor](https://tremor.so)、[xterm.js](https://xtermjs.org) |
| 数据库 | PostgreSQL 16 + [TimescaleDB](https://www.timescale.com) |
| 监控 Agent | Go + [gopsutil](https://github.com/shirou/gopsutil) |
| 部署 | Docker Compose |

## AI 集成

仓库中包含 [OpenCode skill](../skills/talus/SKILL.md)。加载此 skill 的 AI 助手可通过 Talus REST API 管理服务器、执行命令、查询指标和创建带作用域的 API 密钥——无需手动查阅 API 文档。

```bash
# 在任意 AI 会话中（已打开 Talus 仓库）：
"通过 Talus 列出所有服务器"
"查看 web-01 的 CPU 指标"
"创建一个只读的监控 API Key"
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

## 本地构建

如果不使用预构建的 GHCR 镜像，可以在 `docker-compose.yml` 的 `hub` 服务下添加 `build:` 块：

```yaml
hub:
    build:
      context: .
      dockerfile: backend/Dockerfile
    # image: ghcr.io/molicherry/talus:latest  # 注释掉 image 行
```

然后运行：

```bash
docker compose build
docker compose up -d
```

## 许可证

[MIT](../LICENSE)
