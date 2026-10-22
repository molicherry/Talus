# Talus

> 你的服务器舰队的青铜守卫。在一个地方完成监控、命令执行和远程连接。

**Talus** 是一个自托管的单用户 Linux 服务器管理平台。通过 SSH/SFTP 自动安装常驻监控 Agent，节点主动通过 WSS/HTTPS 上报，无需新增节点入站端口。

## 功能

- **服务器清单** — 按名称、IP 和 SSH 端口注册你的 VPS 和裸金属主机。
- **SSH 连接方式** — 每台服务器可选直连、单个 SSH 跳板或 SOCKS5，命令执行、浏览器终端、文件和 Agent 安装统一使用所选路线；常驻监控使用独立的出站连接。
- **凭证保险库** — 使用 AES-256-GCM 加密存储 SSH 密码和私钥。
- **远程 Shell** — 在任意服务器上执行命令，查看 stdout、stderr 和退出码。
- **交互式终端** — 在浏览器中打开完整的 PTY 会话（xterm.js，支持窗口缩放、二进制输出和活跃会话保活），中文及 Emoji 跨帧输出可连续解码。
- **实时监控** — CPU、内存、磁盘、负载、Swap、网络和磁盘 I/O 图表，支持多时间范围、平均值与峰值视图及数据表格。常驻 Agent 默认每 5 秒采样，通过 WSS 上报，支持 HTTPS 回退、断线持久缓冲和历史补报；页面分别显示连接、采集及数据状态。
- **API 密钥** — 创建带作用域和服务器级别访问控制的 API 密钥。二维权限模型（操作 × 服务器）。密钥加密存储，支持按需复制，带有审计日志和速率限制。
- **SFTP 文件管理** — 在 SSH 工作区浏览文件、上传下载与续传、传输目录，以及编辑 UTF-8 文本并检查版本冲突。文件 API 复用 SSH 凭据、已确认的主机指纹和代理路线。
- **服务代理** — 注册外部服务（Grafana、Portainer 等），加密存储凭据。通过 Talus 代理 API 请求，支持凭据注入和占位符替换。编辑时可加载已有凭据，支持显示/隐藏切换和复制。

## 架构

终端使用 `talus.terminal.v2` WebSocket 子协议，输出为 SSH 原始字节的二进制帧；认证、输入、窗口调整、状态及错误仍使用 JSON。调用方需按 UTF-8 字节流连续解码，不能逐帧单独转换。未指定子协议的旧客户端继续接收 JSON 输出，服务端会保留读取边界上不完整的 UTF-8 字符。

会话就绪后，每 25 秒发送 WebSocket Ping 和 SSH keepalive。浏览器自动回复 Pong；SSH 探测超过 10 秒未收到响应，或 WebSocket 超过 35 秒未收到 Pong，会结束会话并丢弃连接。SSH 明确拒绝探测请求也视为连接存活。断开、取消或撤销会话时，会停止探测并等待相关工作退出。

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

- **管理与监控分离**：Hub 通过 SSH 执行管理和 Agent 安装；常驻 Agent 主动连接 `AGENT_HUB_URL` 上报监控。
- **常驻 Agent**：校验后的程序作为 systemd 系统服务或用户服务运行。本地私有目录保存最多 24 小时 / 64 MiB 的待发送样本，只有收到事务落库确认后才删除。请先设置节点可访问的 Hub HTTPS 地址。安装要求运行中的 systemd 和 `flock`；非 root SSH 账号须启用用户 lingering，Talus 不会自动 sudo 提权。
- **凭证加密**：使用你掌控的主密钥进行静态加密存储。列表响应不返回秘密值，已认证的管理员可通过专用接口查看，并记录审计日志。

## 快速开始

### 前置条件

- Docker 和 Docker Compose
- 可通过 SSH 访问的 Linux 服务器（推荐 Debian/Ubuntu）；文件传输及监控 Agent 首次部署或更新需要启用 SSH 的 `sftp` 子系统。

### 1. 克隆仓库

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
```

### 2. 配置

复制 `.env.example` 为 `.env` 并设置真实值——未设置时服务无法启动：

```bash
cp .env.example .env
```

```env
DB_PASSWORD=<你的数据库密码>
VPSMANAGER_MASTER_KEY=<使用 openssl rand -hex 32 生成>
JWT_SECRET=<使用 openssl rand -hex 32 生成>
```

需要监控时，将 `.env` 中示例 `AGENT_HUB_URL` 替换为受管节点能够访问的真实 Hub
HTTPS 地址。不使用监控的新部署应设置 `AGENT_HUB_URL=`，不要保留示例域名：
该值非空就会启用 Agent 自动安装尝试。Agent 安装需要运行中的 systemd、`flock`
和 SFTP；非 root SSH 账号还需启用用户 lingering。

### 3. 启动（源码构建）

仓库内的 `docker-compose.yml` 从源码构建 Talus：

```bash
docker compose up -d --build
```

访问控制台：**http://localhost:8080**

首次登录时，输入任意用户名和至少 8 位的密码，系统会自动创建管理员账户。

### 4. 添加服务器

1. 进入 **Credentials**（凭证）→ **Add Credential**（添加凭证），保存 SSH 用户名及密码或私钥。
2. 进入 **Servers**（服务器）→ **Add Server**（添加服务器），填写名称、主机、SSH 端口和描述，并选择该凭证。
3. 测试已保存的连接，将显示的主机指纹与可信来源核对，然后在 Web UI 确认。跳板和目标主机须分别确认。
4. 执行命令或打开终端验证 SSH 访问。需要监控时，等待 Agent 自动部署，并分别检查连接、采集及数据新鲜度。

需要通过白名单出口访问时，在服务器表单选择“连接方式”：

- **SSH 跳板**：选择已登记、有 SSH 凭据的直连服务器；跳板的 sshd 需要允许到目标地址的 TCP 转发。
- **SOCKS5**：填写代理地址和端口，可选账号密码认证。

服务器的主机、SSH 端口和凭据仍填写真实目标的信息。目标防火墙应放行跳板或代理的实际出口 IP。目标域名由跳板或代理解析，代理失败不会自动回退直连。

保存后，在编辑页点击“测试连接”验证新的完整 SSH 握手。测试总预算取 `SSH_TIMEOUT` 与 10 秒的较小值，包含配置读取及并发槽等待。代理密码加密保存，普通读取不会返回；编辑相同代理时密码留空保留，修改代理地址或用户名则需重新输入。连接配置变更用于后续操作，已打开的终端重新连接后生效。

修改连接方式和测试连接需要管理员登录。API 密钥仍可通过管理员预配置的路线操作其有权限的目标。启用代理路线前应停止旧版本 Hub；旧二进制不理解新配置，仍会直连。

### 5. 代理外部服务

1. 进入 **Services**（服务）→ **Add Service**（添加服务）
2. 填写名称、显示名称、基础 URL（如 `http://localhost:3000`）和凭据（键值对）
3. 可选：将服务关联到某台服务器，用于管理和授权范围；Hub 直接请求服务的基础 URL，不使用 SSH 隧道
4. 使用 relay API 通过 Talus 代理请求——凭据自动注入，`{{key}}` 占位符自动替换。不传 `mode` 时总预算为 30 秒；显式传入 `"mode": "bounded_stream"` 可启用最长 300 秒的流式请求，响应头等待和无进展空闲上限均为 30 秒，`mode` 不转发给上游

### 6. 查看使用日志

管理员可进入 **使用日志**，筛选操作历史并查看详情。日志覆盖资源变更、敏感信息查看、SSH 命令执行、服务转发和终端会话，不记录密钥等秘密信息、命令内容或流式传输内容。API 密钥无法查询这些日志。

### 7. 通过 SFTP 管理文件

打开 SSH 终端工具栏中的“文件”。桌面使用分栏，手机在终端和文件面板间切换，保留原 SSH 会话。终端、文件面板和文本编辑器统一跟随浅色、深色或系统主题，切换时保留终端内容和编辑草稿。

文件管理复用 SSH 账号、端口、加密凭据、主机指纹确认、直连 / SOCKS5 / 跳板路线和连接池配额。目标须启用 SSH 的 `sftp` 子系统，无需额外安装 FTP 服务或 Agent。内部文件上传和监控 Agent 部署也共用这套 SFTP 传输层，原 shell 上传方式已替换，不会自动回退。Agent 上传、SHA256 校验和原子激活始终使用同一条锁定的 SSH 连接；上传时文件权限为 `0600`，激活时再设置可执行权限。文件权限取决于 SSH 账号，Talus 不提供目录沙箱或 `sudo` 提权。

支持路径导航、上传下载、新建目录、改名或移动、删除文件或空目录。覆盖上传需要确认。新文件权限为 `0600`；替换文件保留原权限位，所有者为创建临时文件的 SSH 账号。覆盖要求服务器支持 `posix-rename@openssh.com`，不会通过先删除原文件来降级覆盖。新文件优先通过原子硬链接发布，否则使用 SFTP v3 的禁止覆盖重命名约定。

上传按 1 MiB 分块，并校验整个文件的 SHA256。“取消”暂停上传，重新选择同一文件即可从服务器确认的实际偏移继续；“放弃未完成上传”删除临时文件，保留目标文件。当前标签页刷新后仍保留上传恢复信息；退出登录、会话过期或切换账号会清除这些信息。签名令牌有效期 24 小时，绑定用户或 API 密钥身份、服务器、SSH 路线、目标路径、大小和校验和。`JWT_SECRET`、凭据和连接配置不变时，Hub 重启后仍可恢复；令牌过期后，同一调用者仍可通过原路线清理临时文件。路线变化后遗留的临时文件可由具备删除权限的账号通过普通文件管理清理。

下载以 16 MiB 为块，通过 `Range`、`If-Range` 和 SHA256 ETag 校验版本与内容。文件面板保持打开时，可从上一个完整块继续；关闭面板或刷新页面会释放浏览器中暂存的数据，重新下载须从头开始。

“上传目录”保留相对子目录，不会自动覆盖已有文件；失败或取消前已成功上传的文件保留。浏览器仅提供选中的文件，空本地目录不会上传。“下载目录 ZIP”包含嵌套目录和空远程目录，跳过符号链接及特殊文件，并在开始响应前检查大小限制。符号链接可展示、改名和删除，但不主动跟随它们进行文件下载、编辑或覆盖；需要文件系统隔离时，应在目标 SSH 账号或 chroot 上配置。

“编辑文本”支持最大 1 MiB 的 UTF-8 文本，保留换行和权限位，保存时携带内容哈希版本。版本过期返回 `409 sftp_conflict`，界面保留草稿；关闭或重新加载未保存的内容须确认丢弃。保存通过同目录临时文件发布。SFTP 不支持条件重命名，因此版本检查属于乐观并发控制，外部写入仍可能发生在最终检查与重命名之间。

传输和清理限制：

- 单文件、目录 ZIP 内容总量及一次目录上传总量均不超过 64 MiB；ZIP 元数据额外不超过 2 MiB。文本编辑上限 1 MiB。
- 目录列表最多返回 2,000 项或 2 MiB 元数据，目录协议输入限制为 8 MiB；ZIP 最多 2,000 项、32 层子目录。超过限制时返回错误或明确标记列表已截断。
- 每台服务器最多一个活跃 SFTP 文件 API 操作，与现有 SSH 配额共享限制；终端和监控使用各自的传输连接。SSH 通道及工作协程退出后才释放文件操作槽。
- 元数据操作预算为 25 秒，传输总预算为 10 分钟，HTTP 读写空闲超时为 30 秒。反向代理须允许相应大小和时长。
- 普通上传在可连接远端的失败或取消时清理临时文件；网络中断可能留下临时文件。可续传上传主动保留临时文件供恢复或放弃，发布前保留原目标文件；令牌过期不代表远端临时文件被自动删除。

## 技术栈

| 层级 | 技术 |
|------|------|
| 后端 | Go、[chi](https://github.com/go-chi/chi)、[GORM](https://gorm.io)、[gorilla/websocket](https://github.com/gorilla/websocket)、[golang-jwt](https://github.com/golang-jwt/jwt) |
| 前端 | React 19、TypeScript、[Vite](https://vite.dev)、[Tailwind CSS](https://tailwindcss.com)、[xterm.js](https://xtermjs.org) |
| 数据库 | PostgreSQL 16 + [TimescaleDB](https://www.timescale.com) |
| 监控 Agent | Go + [gopsutil](https://github.com/shirou/gopsutil) |
| 部署 | Docker Compose |

SSH 工作区的文件面板、二进制传输、进度与取消、临时文件发布等交互思路参考了 [EdgeSSH](https://github.com/aozorae/EdgeSSH)，协议由 Talus Go 后端通过 `github.com/pkg/sftp` 实现。

## AI 集成

Talus 附带一个可移植的 [agent skill](../skills/talus/SKILL.md)，通过 REST API 完成
服务器管理、SSH 命令、监控查询、SFTP 和已注册服务调用。AI 运行环境需要能够读取
skill，并使用终端中的 `curl` 或等效 HTTP 工具发送请求。MCP 为可选接入方式。

### 1. 准备 Talus 实例

按照[快速开始](#快速开始)或[生产部署](#生产部署ghcr-镜像)部署 Talus，并完成首次
管理员登录；已有实例可直接使用。添加需要交给 AI 操作的服务器及凭据，并在 Web UI
确认 SSH 主机指纹。需要调用外部服务时，在 **服务** 中登记服务、凭据和使用指南。

选择 **AI 实际运行环境能够访问**的 Talus 地址。`localhost` 指向执行请求的环境；
云端任务或容器中的 `localhost` 通常无法访问你电脑上的 Talus 实例。

### 2. 创建 API 密钥

进入 **Talus Web UI → API Keys**，为 AI 创建密钥。按实际任务选择作用域，
并根据需要限制可访问的服务器：

| 任务 | 应选择的作用域 |
| --- | --- |
| 查看服务器与监控 | `servers:read`、`metrics:read` |
| 查询服务目录及使用指南 | `services:read` |
| 调用已注册服务 | `services:read`、`services:relay` |
| 执行命令 | `servers:read`、`servers:exec` |
| 管理文件 | `servers:read`，以及所需的 `files:read`、`files:write`、`files:delete` |
| 添加/更新服务器 | `servers:read`、`servers:write`；查询已有凭据还需 `credentials:read` |

默认选择包含 `servers:read`、`servers:exec`、`servers:terminal`、`metrics:read`、
`credentials:read` 和 `services:read`，并非只读权限。`servers:write`、
`services:relay` 及文件作用域需主动勾选。不限制服务器意味着可访问全部服务器。
创建凭据或服务、确认主机指纹等操作仍需 Web UI，详见
[skill 权限说明](../skills/talus/SKILL.md#authentication-and-permissions)。

将此页面提供的 API 密钥原文用于 `TALUS_API_KEY`。它与管理员密码、SSH 凭据及
Hub 的 `JWT_SECRET` 是不同的配置。

### 3. 安装完整 skill

从本仓库或下载的源码压缩包中取得 `skills/talus/`。按照所用 AI 工具的技能加载机制，
导入整个 `talus` 目录，或放入其支持的 skills 目录。具体目录位置和重新加载方式
以该工具自身的文档为准。安装后的结构应为：

```text
talus/
├── SKILL.md
└── references/
    ├── files.md
    └── metrics.md
```

安装位置必须能被 AI 会话读取；只安装在 Talus 服务器上，不会自动提供给另一个环境中
运行的 AI。保留 `SKILL.md` 同级的 `references/`，安装后不需要保留应用仓库的其余文件。
安装完成后，按 AI 工具要求重新加载技能或开启新会话。更新时替换完整 `talus/` 目录并
重新加载；实例配置保存在 AI 运行环境中，使更新 skill 时能够保留配置。

### 4. 配置 AI 运行环境

在 **AI 执行 HTTP 请求的环境**中设置：

| 变量 | 填写内容 |
| --- | --- |
| `TALUS_URL` | 可访问的 Talus 基础地址，例如 `https://talus.example.com`，不含 `/api/v1`；默认 `http://localhost:8080` |
| `TALUS_API_KEY` | 上一步创建的 API 密钥原文 |

通过 Bash 终端启动 AI 时，可先替换示例地址，再通过隐藏输入提示填写密钥：

```bash
export TALUS_URL='https://talus.example.com'
read -r -s -p 'Talus API key: ' TALUS_API_KEY
printf '\n'
export TALUS_API_KEY
```

在这个终端中执行下方验证命令，然后从同一终端启动 AI，使其继承这些变量。
`export` 只作用于当前终端和之后启动的子进程；已启动的桌面应用以及远程、云端会话
不会自动继承。此类环境应通过其运行环境变量或密钥配置功能设置，再按要求重启或重新
加载。需要持久配置时，也应使用运行环境提供的配置机制。

Hub 部署用的 `.env` 不会自动配置 AI。`AGENT_HUB_URL` 配置在 Hub 上，供受管节点
访问；`TALUS_URL` 配置在 AI 环境中，供 AI 访问。不要将 API 密钥写入 skill 文件或贴到聊天中。

### 5. 验证连接和 skill 加载

先从 AI 的执行环境访问无需鉴权的版本接口，确认返回 HTTP 200 和 Talus 版本信息：

```bash
curl --silent --show-error --fail-with-body "${TALUS_URL%/}/api/v1/version"
```

再验证密钥和 `servers:read` 权限：

```bash
curl --silent --show-error --fail-with-body \
  "${TALUS_URL%/}/api/v1/servers" -H "X-API-Key: $TALUS_API_KEY"
```

预期返回 HTTP 200 和 `{"data":[...]}`；`{"data":[]}` 也是有效的空结果。
如果密钥仅用于服务、未授予 `servers:read`，可改用 `/api/v1/services` 验证
`services:read`。这一步验证 API 可用，还不能证明 skill 已加载。

确认 AI 会话已通过其技能机制加载 `talus`，再输入：

> 使用 talus skill 和已配置的环境变量，通过真实 API 请求列出我的服务器，报告名称和状态，不要显示 API 密钥。

服务专用密钥可改为要求列出已注册服务并读取所选服务的使用指南。确认 AI 读取了 skill
并实际发出了请求；只回答调用方法或示例代码，不算验证完成。

### 6. 常见问题

| 现象 | 检查方法 |
| --- | --- |
| 找不到 skill 或参考文档 | 确认完整 `talus/` 目录在 AI 环境中可读，且当前会话已加载。 |
| AI 读不到 URL 或密钥 | 检查实际执行进程的环境变量或密钥配置，并重启/重新加载；验证时不要打印密钥。 |
| 连接被拒绝、域名解析失败或超时 | 从 AI 环境检查地址、端口和网络访问，确认 `localhost` 指向预期主机。 |
| TLS 证书错误 | 修正证书或配置运行环境信任的 CA。 |
| 401 | 检查实例地址、密钥原文以及密钥是否已撤销。 |
| 403 | 检查作用域、服务器限制及是否属于 Web UI 专用操作；能列出服务不代表拥有 `services:relay`。 |
| SSH 主机指纹未确认或发生变化 | 在 Web UI 核对并确认主机指纹。 |
| 监控无数据或数据过期 | 检查节点可访问的 `AGENT_HUB_URL`、Agent 部署条件，以及连接、采集、数据状态。 |

### 服务：已注册服务优先走 Talus relay

已注册服务（Dokploy、Portainer 或任意内部应用）由 Talus 代理。它们的 `base_url`
须能被 Hub 访问，可以保留为内网地址；AI 只需能够访问 Talus。凭据按服务使用指南
指定的方式替换到请求中。在服务器上操作某个服务时，
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

### 文件 API

使用用户 JWT（`Authorization: Bearer …`）或 API 密钥（`X-API-Key: …`）认证。所有接口校验 API 密钥的 `server_ids`。文件作用域需单独授予，已有密钥不会自动获得文件权限。具备 Exec 或终端权限的调用方仍拥有底层 SSH 账号的命令能力。

| 作用域 | 操作 |
| --- | --- |
| `files:read` | 列表、属性、下载、读取文本、目录 ZIP |
| `files:write` | 上传、续传及状态、清理自身上传的临时文件、新建目录、改名、保存文本 |
| `files:delete` | 删除文件、符号链接或空目录 |

API 密钥无法确认首次或变更后的主机指纹，须先由管理员在 Web UI 核对并信任。目标不支持 SFTP 时会明确报错，不会回退为执行 shell 命令。

以下接口统一位于 `/api/v1/servers/{id}/sftp` 下。查询参数中的远程路径须 URL 编码，也可通过对应 JSON 请求体传递。普通响应为 `{"data": …}`，错误为 `{"error":{"code":…,"reason":…,"message":…}}`；文件和 ZIP 下载返回二进制。上传体直接发送原始字节，无需 base64 或 JSON 包装。

| 方法和路径 | 请求 | 响应 |
| --- | --- | --- |
| `GET /entries?path=…` | 默认路径 `.` | `path`、`parent`、`entries`、`truncated` |
| `GET /stat?path=…` | 远程路径 | `name`、`path`、`kind`、`size`、`mode`、`modified_at` |
| `GET /download?path=…` | 可选 `Range`、`If-Range` | 二进制、`ETag`、`Accept-Ranges`、`Content-Length`；分段响应为 `206` 并带 `Content-Range` |
| `POST /upload?path=…&overwrite=false` | 原始字节；`Content-Length` 或 `size` 参数 | 上传大小；单次上传，取消时尽可能清理临时文件 |
| `POST /mkdir` | `{"path":"…","parents":false}` | 成功；`parents:true` 创建缺失的父目录 |
| `POST /rename` | `{"from":"…","to":"…"}` | 成功；目标路径必须不存在 |
| `DELETE /entry?path=…` | 远程路径 | 删除文件、链接或空目录，不递归删除 |
| `GET /archive?path=…` | 远程目录 | 流式 ZIP 附件 |
| `GET /text?path=…` | 远程文本文件 | `path`、`content`、`version` |
| `PUT /text` | `{"path":"…","content":"…","version":"sha256:…"}` | 新内容和版本；版本过期返回 `409` |
| `POST /uploads/start` | `{"path":"…","size":123,"sha256":"64 个十六进制字符","overwrite":false}` | 签名 `token`、`path`、`size`、`offset`、`sha256`、`expires_at` |
| `POST /uploads/status` | `{"token":"…"}` | 实际偏移及元数据；`completed:true` 可恢复丢失的完成确认 |
| `PUT /uploads/chunk?offset=…&size=…` | 最多 1 MiB 原始字节，`X-Upload-Token` 请求头 | 已确认的远端偏移；偏移过期返回 `409` |
| `POST /uploads/complete` | `{"token":"…"}` | 校验总大小和校验和后发布；完成确认丢失时可重试 |
| `DELETE /uploads` | `X-Upload-Token` 请求头 | 清理该上传者的临时文件 |

每次分块、状态查询和完成请求都重新认证。恢复要求同一 JWT 用户或同一 API 密钥身份，角色相同不代表可以互用令牌；更改路线或凭据会使原上传令牌无法用于新路线。分块失败后先查询 `/uploads/status`，因为部分字节可能已到达远端。

目录条目的 `kind` 为 `file`、`directory`、`symlink` 或 `other`。大小和修改时间不保证不存在并发外部写入；编辑使用内容版本，续传下载使用 `If-Range`。远程读取失败可能在响应头发送后中止连接，调用方应校验 `Content-Length`、校验和或 ZIP 完整性。

使用日志记录操作、调用者、服务器、结果、路线及受限字节计数，不记录文件内容、恢复令牌或远程路径字符串。

#### 文件 API 示例

```bash
# 列出目录。
curl --get "$TALUS_URL/api/v1/servers/1/sftp/entries" \
  -H "X-API-Key: $TALUS_API_KEY" --data-urlencode 'path=/home/deploy'

# 上传原始二进制；默认保留已存在的目标文件。
curl -X POST "$TALUS_URL/api/v1/servers/1/sftp/upload?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -H 'Content-Type: application/octet-stream' \
  --data-binary @data.bin

# 下载；续传时保存并使用 ETag / If-Range，以免混入不同版本。
curl "$TALUS_URL/api/v1/servers/1/sftp/download?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -o data.bin
```

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
| `VPSMANAGER_MASTER_KEY` | *(必填)* | 由密码学安全随机源生成的十六进制主密钥（至少 32 字节）；升级时须保持不变 |
| `JWT_SECRET` | *(必填)* | JWT 签名密钥 |
| `PORT` | `8080` | HTTP 服务端口 |
| `LOG_LEVEL` | `info` | 日志级别：`debug`、`info`、`warn`、`error` |
| `DB_MAX_OPEN_CONNECTIONS` | `32` | 数据库最大连接数，最小为 `12` |
| `USAGE_LOG_WRITE_CONCURRENCY` | `4` | 使用日志写入并发上限 |
| `USAGE_LOG_ACTIVE_LIMIT` | `4096` | 内存中同时跟踪的活动操作数量上限 |
| `USAGE_LOG_RETENTION_DAYS` | `30` | 普通使用日志保留天数 |
| `USAGE_LOG_SENSITIVE_RETENTION_DAYS` | `90` | 敏感操作和删除操作日志保留天数 |
| `USAGE_LOG_LEGACY_WRITERS_DRAINED` | `false` | 仅在所有旧实例及其事务停止后设为 `true`，启用历史审计日志回填；确认回填完成后可移除 |
| `MONITOR_INTERVAL` | `5` | 常驻指标采样间隔，支持 1–60 秒 |
| `AGENT_HUB_URL` | 未设置 | 节点能够访问的 Hub HTTPS 地址，自动部署常驻监控前必须配置 |
| `AGENT_ALLOW_HTTP` | `false` | 显式允许可信测试或内网使用 HTTP |
| `MONITOR_TOLERATED_ROUNDS` | `1` | 旧 SSH 采集设置；常驻模式使用 45 秒联系超时 |
| `MONITOR_ALLOWED_LATENCY_SECONDS` | `0` | 旧 SSH 采集设置；常驻数据新鲜度为三个采样周期，最少 15 秒 |
| `AGENT_ARTIFACTS_DIR` | `/usr/local/bin` | 包含 `agents-manifest.json` 及 Linux amd64/arm64 双架构 Agent 的目录，启动时校验 |
| `METRICS_RETENTION_ENABLED` | `false` | 启用超过 35 天的合格原始指标及聚合指标清理，实际删除还须关闭演练模式 |
| `METRICS_RETENTION_DRY_RUN` | `true` | 仅报告清理计划；只有 `METRICS_RETENTION_ENABLED=true` 且本值为 `false` 才会删除 |
| `SSH_TIMEOUT` | `10` | SSH 连接超时（秒） |
| `EXEC_TIMEOUT` | `30` | 默认命令总预算（秒），包含 SSH 建连和执行；请求可覆盖，但不超过配置上限（默认 300 秒） |
| `EXEC_TIMEOUT_MAX` | `300` | 命令总预算上限（秒），须不小于 `EXEC_TIMEOUT` 且不超过 `300` |
| `EXEC_OUTPUT_LIMIT` | `8388608` | 保留的 stdout 与 stderr 合计字节上限，须为正数且不超过 8 MiB |
| `EXEC_OUTPUT_STREAM_LIMIT` | `4194304` | 单个输出流保留的字节上限，须为正数、不超过 4 MiB，且不大于 `EXEC_OUTPUT_LIMIT` |

最近联系超过 45 秒视为不可达；数据超过三个采样周期（最少 15 秒）视为过期。采集异常及写库失败分别展示。

### 监控 Agent 升级

常驻监控不再按次调度 SSH 采集。安装和修复使用独立 SSH lease 与 3 分钟预算，全局最多 4 个并发部署，每个节点最多一个任务，失败至少等待一分钟后重试。未设置 `AGENT_HUB_URL` 时显示需要配置，已有指标历史保留。

程序、配置和服务文件上传后先进行 SHA256 校验，再激活。兼容旧 Agent 在新版本准备期间继续上报。新服务重启后必须通过与 Hub 的认证联系检查；激活失败时恢复已有的程序、配置和服务文件。

删除服务器时会卸载常驻 Agent 并清理记录的安装位置；即使 `AGENT_HUB_URL` 已被清空，或 SSH 凭据已换成其他账号也同样生效。安装位置会在远端文件变更前保存，直到新服务健康且旧安装清理成功后才完成迁移；清理或记录保存失败时，部署保持失败状态并允许重试。root 能停止旧账号的用户服务，降级后的普通账号无法停止旧服务时，迁移不会被标记成功。没有账号记录的历史安装会先在节点上发现实际位置；root 会检查各账号的主目录，不再假定服务属于当前登录账号。并发删除未取得清理权时，只有服务器已经删除才返回成功，否则返回冲突供重试。

跨账号的用户安装清理会通过 `runuser` 全程以安装账号运行；无法解析或切换到该账号时，会保留其文件并报告清理失败。锁文件不会被截断，符号链接或非普通锁文件会被拒绝。指向同一目录的 HOME 别名会保留正在运行的安装和 outbox；同一账号改用不同的实际 HOME 时，会在远端文件变更前拒绝迁移，须先手动迁移该账号的安装。

### Docker 升级与加密数据

保留现有数据库卷和原始 `.env`，尤其是 `VPSMANAGER_MASTER_KEY`。替换前先停止旧 Hub；升级按单 Hub 部署设计，需要短暂维护窗口。数据库咨询锁会让新版本 Hub 串行执行迁移，但无法阻止旧二进制继续写入。

使用预构建镜像的 Compose 部署，先在 Compose 配置中选定新镜像标签，再执行：

```bash
docker compose stop hub
docker compose exec -T db pg_dump -U vpsmanager -d vpsmanager -Fc > talus-before-upgrade.dump
docker compose pull hub
docker compose up -d --no-deps hub
docker compose logs -f hub
```

确认备份能够配合原始主密钥恢复，不要删除数据库卷。源码构建部署无需拉取镜像，改用 `docker compose up -d --no-deps --build hub` 启动新版本。

Hub 启动时会添加内部加密版本字段，自动升级 SSH 凭证、服务凭据、SOCKS5 密码及可恢复原文的 API 密钥。迁移逐条处理记录，验证新密文后，原子提交盐值、密文和版本；软删除记录也包含在内。没有可恢复原文的历史 API 密钥保留已有认证哈希，无需重新签发。迁移完成后才启动 HTTP 服务、监控和 SSH 连接池。迁移期间 Docker 健康检查可能保持 `starting`，数据量较大时应留足启动时间。

迁移失败时 Hub 停止启动，日志只记录表名、记录 ID 和失败阶段，不记录秘密值。保留原始主密钥及数据，修复原因后重启：已提交的记录会校验并跳过，其余记录继续迁移。后续启动也会校验已有 v2 密文，包括从备份恢复后的首次启动。

只要已有一条记录升级，原来仅支持 Argon2id 的镜像就无法读取全部凭据，即使迁移随后失败也一样。回滚须使用原始主密钥恢复升级前的数据库备份，备份之后的修改会丢失。仅切换镜像标签或更换主密钥无法完成回滚。反向迁移需要另行提供兼容工具。

## 安全性

- SSH 私钥和密码、SOCKS5 密码、服务凭据及可恢复的 API 密钥原文使用 **AES-256-GCM** 静态加密。新记录通过 **HKDF-SHA256** 从 `VPSMANAGER_MASTER_KEY` 派生密钥，并按用途隔离。主密钥必须由密码学安全随机源生成（`openssl rand -hex 32`）。登录密码继续使用 bcrypt。旧版 Argon2id 加密记录会在 Hub 对外提供服务前自动升级。
- 列表响应不返回凭证或 API 密钥原文。专用查看接口仅允许 JWT 认证，按用户限流（每分钟 5 次）并记录审计日志。
- 目标服务器**无需开放额外端口**——监控数据通过现有的 SSH 通道传输。
- 监控 Agent 作为 systemd 常驻服务运行，通过出站 WSS/HTTPS 上报；Hub 通过 SSH/SFTP 安装或更新程序，并在激活前校验文件。节点无需新增监控入站端口。
- 修改密码会使已有 JWT 失效，并主动关闭该用户的终端会话。API 密钥使用独立的生命周期。
- **SSH 主机指纹显式确认**：首次连接仅读取并展示 SHA256 指纹，在发送 SSH 认证信息前中止。管理员通过可信渠道核对并确认后才能连接；跳板机和目标主机分别确认。已有可信密钥继续有效，密钥变化时会阻止认证并要求重新确认。确认请求校验展示的指纹和连接版本，拒绝过期操作。

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
      AGENT_HUB_URL: ${AGENT_HUB_URL:-}
      AGENT_ALLOW_HTTP: ${AGENT_ALLOW_HTTP:-false}
      MONITOR_INTERVAL: ${MONITOR_INTERVAL:-5}
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

部署前，复制 `.env.example` 为 `.env` 并填写下列密钥。需要监控时，将 `AGENT_HUB_URL`
设为节点可访问的真实 Hub HTTPS 地址；不需要监控时留空（`AGENT_HUB_URL=`）。
不要保留示例域名，否则会启用 Agent 自动安装尝试。

```bash
cp .env.example .env
# 编辑 .env：填写 DB_PASSWORD / VPSMANAGER_MASTER_KEY / JWT_SECRET；
# 将 AGENT_HUB_URL 设为真实 Hub 地址，或留空。
docker compose -f docker-compose.prod.yml up -d
```

**升级到新版本：**

```bash
docker compose -f docker-compose.prod.yml stop hub
docker compose -f docker-compose.prod.yml exec -T db pg_dump -U vpsmanager -d vpsmanager -Fc > talus-before-upgrade.dump
docker compose -f docker-compose.prod.yml pull hub
docker compose -f docker-compose.prod.yml up -d --no-deps hub
```

从尚未支持 JWT 撤销的版本首次升级时，须先停止全部旧 Hub 实例并结束旧 WebSocket 会话，再启动新版本，不能让两类版本并行服务。后续升级和回滚均须保留令牌版本校验与主动会话撤销。

### 反向代理超时

Talus 使用路由级写入预算：普通 HTTP API 为 15 秒，Exec 最长执行 300 秒，显式开启的 Relay 流式请求最长 300 秒。前置代理须额外预留结果返回及清理时间：

| 路由 | 代理超时下限 |
| --- | --- |
| `POST /api/v1/servers/*/exec`、`POST /api/v1/services/*/relay` | ≥ 330 秒 |
| SFTP 元数据 / 传输 | ≥ 30 秒 / ≥ 630 秒 |
| 其他普通 HTTP API | ≥ 15 秒 |

SFTP 传输总预算为 10 分钟，HTTP 读写空闲超时为 30 秒。代理须允许至少 64 MiB 的文件请求体，并对 SFTP 文件和 ZIP 下载关闭响应缓冲。

`/api/v1/services/*/relay` 应关闭响应缓冲，以便增量转发流式内容。终端路由须启用 WebSocket 升级，并按预期交互会话时长配置空闲超时；上表中的普通 HTTP 超时不代表终端会话时限。

## 测试

通过[统一测试入口](../tests/README.md)运行 `fast`、`integration`、`ui`、`e2e` 套件及 M1–M4 门禁。[测试索引](../tests/coverage.md)由机器清单生成，列出场景、实现和执行命令。

M1 的部署检查会使用临时数据库和本机回环 HTTP 服务启动当前 Hub，自动验证初始化、重启、JWT 撤销、服务目录、使用指南和 Relay 行为，无需个人服务器、公网域名或历史部署记录。运行前将 `TEST_DATABASE_URL` 指向隔离的 TimescaleDB 测试实例，并确保测试账号有创建临时数据库及安装 TimescaleDB 扩展的权限。缺少数据库或跳过必需测试都会阻断门禁。

```bash
bash tests/run.sh --gate M1
```

## 版本发布

维护者请按 [Release 与 Tag 规范](releasing.zh-CN.md)执行版本选择、草稿审核、
不可变镜像发布及 `latest` 故障恢复。

## 许可证

[MIT](../LICENSE)
