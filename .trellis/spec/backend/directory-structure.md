# Directory Structure

> How backend code is organized in this project.

---

## Overview

Monorepo with Go backend. Hub service (REST + WebSocket) in `cmd/server/`, monitoring agent in `cmd/agent/`. All business logic under `internal/` — Go's visibility boundary ensures external packages can't import internals.

---

## Directory Layout

```
backend/
├── cmd/
│   ├── server/                  # Hub 主服务入口
│   │   └── main.go              # 配置加载 → 依赖注入 → 启动 HTTP/WS
│   └── agent/                   # 监控 Agent（独立二进制，部署到目标服务器）
│       ├── main.go
│       └── collector/
│           ├── cpu.go           # CPU 使用率采集
│           ├── memory.go        # 内存使用率采集
│           ├── disk.go          # 磁盘使用率采集
│           ├── network.go       # 网络流量采集
│           └── process.go       # 进程列表采集
├── internal/
│   ├── config/                  # 配置加载（环境变量 + YAML fallback）
│   │   └── config.go
│   ├── server/                  # HTTP 路由注册 + 中间件
│   │   ├── router.go            # 所有路由定义入口
│   │   ├── middleware/
│   │   │   ├── auth.go          # JWT + API Key 鉴权中间件
│   │   │   ├── logging.go       # 请求日志中间件
│   │   │   ├── ratelimit.go     # 速率限制中间件
│   │   │   └── cors.go
│   │   └── response.go          # 统一响应格式（JSON 信封）
│   ├── handler/                 # HTTP handlers — 薄层：参数绑定 + 调用 service + 写响应
│   │   ├── auth.go              # POST /auth/login, POST /auth/apikey
│   │   ├── user.go              # 用户 CRUD
│   │   ├── server.go            # 目标服务器 CRUD
│   │   ├── credential.go        # SSH 凭据管理
│   │   ├── terminal.go          # WebSocket PTY 终端代理
│   │   ├── exec.go              # POST /exec 命令执行
│   │   ├── file.go              # 文件上传/下载
│   │   └── metrics.go           # GET /metrics/{server_id}
│   ├── service/                 # 业务逻辑层 — 核心逻辑，不依赖 HTTP
│   │   ├── auth.go              # 登录、API Key 生成/验证
│   │   ├── user.go
│   │   ├── server.go
│   │   ├── credential.go        # SSH 凭据加密存储、解密使用
│   │   ├── ssh.go               # SSH 连接池、会话管理、命令执行
│   │   ├── monitor.go           # 监控数据聚合
│   │   └── audit.go             # 审计日志写入
│   ├── model/                   # GORM 数据模型（DB schema 定义）
│   │   ├── user.go
│   │   ├── server.go
│   │   ├── credential.go        # SSH 密钥/密码（加密存储）
│   │   ├── audit.go
│   │   └── metric.go            # 监控指标（TimescaleDB hypertable）
│   ├── repository/              # 数据访问层 — 裸 SQL 或 GORM，不混业务逻辑
│   │   ├── user.go
│   │   ├── server.go
│   │   ├── credential.go
│   │   ├── audit.go
│   │   └── metric.go
│   └── pkg/                     # 内部共享工具包
│       ├── crypto/
│       │   ├── aes.go           # AES-256-GCM 加密/解密（凭据加密）
│       │   └── key.go           # 主密钥派生（Argon2id）
│       ├── sshpool/
│       │   ├── pool.go          # SSH 连接池
│       │   ├── session.go       # 会话复用
│       │   └── config.go        # SSH 连接配置封装
│       ├── token/
│       │   ├── jwt.go           # JWT 签发/验证
│       │   └── apikey.go        # API Key 生成/验证
│       └── stream/
│           └── ws.go            # WebSocket 双向流工具
├── migrations/                  # SQL 迁移文件（golang-migrate）
│   ├── 000001_create_users.up.sql
│   ├── 000001_create_users.down.sql
│   └── ...
├── Dockerfile                   # 多阶段构建：编译 + 最小运行时（scratch/alpine）
├── docker-compose.yml
├── go.mod
└── go.sum
```

---

## Layer Dependency Rules

```
handler ──► service ──► repository ──► model (GORM)
   │            │             │
   │            │             └──► DB (PostgreSQL)
   │            │
   │            └──► internal/pkg/*     (无限制调用工具包)
   │
   └──► internal/server/response.go    (格式化输出)
   └──► ❌ 不直接调 repository
   └──► ❌ 不直接调 model
```

**单向依赖**：handler → service → repository → model。反向依赖禁止。同级之间不能相互调用（一个 service 不调另一个 service，需要编排时放在 handler 层）。

---

## Module Organization

- 每个领域（user, server, credential, audit）在 handler/service/model/repository 下各有一个同名文件
- 一个新功能按顺序加四层，不能跳过
- handler 只做：参数校验 → 调 service → 写响应。不写 if 分支业务逻辑。
- service 是唯一有业务逻辑的地方。一个 service 方法 = 一个事务或一个完整业务操作。

---

## Naming Conventions

| Type | Convention | Example |
|------|-----------|---------|
| Files | `snake_case.go` | `credential.go`, `ssh_pool.go` |
| Packages | lowercase, single word preferred | `sshpool`, `token` |
| Structs | `PascalCase` | `SSHPool`, `CredentialService` |
| Functions | `PascalCase` (exported) / `camelCase` (unexported) | `NewPool()`, `dialSSH()` |
| Variables | `camelCase` | `sshClient`, `rawKey` |
| SQL tables | `snake_case_plural` | `users`, `ssh_credentials` |
| SQL columns | `snake_case` | `server_id`, `created_at` |
| API routes | `kebab-case` in path | `/api/v1/ssh-credentials` |

---

## Skill Directory (AI Integration)

```
skills/
└── vpsmanager/
    ├── SKILL.md                 # 工具描述、使用说明
    ├── exec-command.ts          # 封裝 POST /api/v1/exec
    ├── list-servers.ts          # 封裝 GET /api/v1/servers
    ├── open-terminal.ts         # 封裝 WS /api/v1/terminal/{id}
    ├── upload-file.ts           # 封裝 POST /api/v1/files/upload
    ├── get-metrics.ts           # 封裝 GET /api/v1/metrics/{id}
    └── types.ts                 # 共享类型定义
```

Skill 是 AI 平台与 Hub 之间的适配层。Hub 保持纯 REST/WS，不耦合任何 AI 平台协议。
