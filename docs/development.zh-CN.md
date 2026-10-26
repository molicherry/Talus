# 开发指南

[English](development.md) | 简体中文

本文说明从新检出的仓库到本地启动的步骤，以及如何为改动选择检查。模块职责见[架构说明](architecture.zh-CN.md)，部署和参数语义由 [README](README.zh-CN.md#配置项)维护，完整测试约定见 [tests/README.md](../tests/README.md)。

## 环境要求

以下命令使用 Linux 或 WSL 2 的 Bash。Docker Compose 是启动完整环境的最简单方式。原生 Windows Agent 验证是独立流程，见 [Windows 监控](windows-monitoring.zh-CN.md#开发验证)；WSL 测试不能代替原生 Windows 验证。

| 工具 | 要求与来源 |
| --- | --- |
| Go | 以 [backend/go.mod](../backend/go.mod) 为准，当前为 Go 1.25.0；依赖需要时允许下载工具链 |
| Node.js 与 npm | 使用与 CI、Docker 构建一致的 Node 24；通过 `npm ci` 安装前端依赖 |
| Docker 与 Compose v2 | 用于数据库及完整环境；镜像验收也需要 Docker |
| 数据库 | 与 [docker-compose.yml](../docker-compose.yml) 一致的 PostgreSQL 16 + TimescaleDB；普通 PostgreSQL 缺少必需扩展 |
| 命令行工具 | Bash、`sh`、`sha256sum`、Git、curl 和 OpenSSL；Agent 构建使用 `sha256sum` |
| 浏览器测试 | 项目中的 Playwright 依赖及 Chromium，运行浏览器检查时安装 |

## 首次通过 Compose 启动

从新检出的仓库开始：

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
cp .env.example .env
openssl rand -hex 32
openssl rand -hex 32
```

编辑 `.env`：设置 `DB_PASSWORD`，将两次独立生成的值分别填入 `VPSMANAGER_MASTER_KEY` 和 `JWT_SECRET`。复用数据库时应保留这些值。`.env` 已被 Git 忽略，不要提交。尚不需要监控时保持 `AGENT_HUB_URL` 为空。

```bash
docker compose up -d --build
docker compose logs -f hub
```

在另一个终端检查健康状态和版本：

```bash
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/api/v1/version
```

打开 `http://localhost:8080`。新数据库首次登录会创建管理员，密码至少八个字符；空仪表盘是正常结果。添加真实节点还需要 SSH 凭据和明确的主机指纹确认，见[添加服务器](README.zh-CN.md#4-添加服务器)。

Compose 会构建源码，但不会监听文件变化。修改后执行 `docker compose up -d --no-deps --build hub` 重建 Hub。`docker compose stop` 保留数据库；需要保留数据时不要执行 `down -v`。

## 分别运行后端和前端

此方式用于 Go 迭代和 Vite 热更新。以下示例均从仓库根目录开始。若 Compose Hub 已占用 8080，先停止该服务；下面的开发数据库与 Compose 数据卷独立。

### 1. 启动开发数据库

```bash
docker run -d --name talus-dev-db \
  -p 127.0.0.1:54329:5432 \
  -e POSTGRES_USER=talus_dev \
  -e POSTGRES_PASSWORD=talus_dev_local \
  -e POSTGRES_DB=talus_dev \
  timescale/timescaledb:latest-pg16
docker exec talus-dev-db pg_isready -U talus_dev -d talus_dev
```

等待 `pg_isready` 显示接受连接。示例凭据仅用于这个绑定本机回环地址的开发数据库。已有容器通过 `docker start talus-dev-db` 复用，不要重复以同名执行 `docker run`。这里没有配置命名卷，替换容器不会自动复用原数据；需要跨容器更换保留数据库时，使用 Compose 的命名卷。

### 2. 构建全部监控 Agent

```bash
sh backend/build/agents.sh dev bin/agents
(cd backend && go run ./cmd/agentverify -version-in-binary bin/agents)
```

脚本相对于 `backend/` 解析输出位置，因此产物位于已被 Git 忽略的 `backend/bin/agents`。Hub 启动时校验 manifest 及 Linux amd64/arm64、Windows amd64 全部程序，即使未启用监控也不能省略。Agent 改动后需重新构建。此流程使用 `dev`；自定义版本构建须保持 Hub、Agent 和 manifest 版本一致。

### 3. 启动 Hub

使用前面配置的 `.env`。其中的值须符合 Bash 赋值语法，包含 shell 特殊字符时应加引号。只加载自己可信的本地配置；Go 进程不会自动读取 `.env`。

```bash
set -a
source .env
set +a
export DATABASE_URL='postgres://talus_dev:talus_dev_local@127.0.0.1:54329/talus_dev?sslmode=disable'
export AGENT_ARTIFACTS_DIR="$(pwd)/backend/bin/agents"
export PORT=8080
unset STATIC_DIR
(cd backend && go run ./cmd/server)
```

这里显式指定开发容器的连接串；`.env` 中的 `DB_PASSWORD` 由 Compose 使用，不参与此连接串。针对已有数据库重启时保留原主密钥。Go 代码修改后重新启动 `go run`，项目没有配置后端自动重载。按前述方式检查 `/healthz` 和 `/api/v1/version`。

### 4. 在另一个终端启动 Vite

```bash
cd frontend
npm ci
VITE_API_BASE_URL=http://localhost:8080 npm run dev -- --host 127.0.0.1
```

打开 Vite 输出的地址。`VITE_API_BASE_URL` 是 Hub 基础地址，不含 `/api/v1` 或末尾斜杠；REST 请求和终端 WebSocket 都使用它。Vite 没有配置 API 代理，省略此值会将请求发到前端服务器。修改环境变量后须重启 Vite。

远程开发环境应使用浏览器可访问的 Hub 地址；浏览器里的 `localhost` 指向浏览器所在机器。被监控节点还需要独立可达的 `AGENT_HUB_URL`。数据库中的已保存设置优先于环境变量，改环境变量却不生效时应检查系统设置。

### 5. 检查生产前端构建

```bash
(cd frontend && npm run build)
```

本地同源访问时，停止 Go 进程，将 `STATIC_DIR` 设置为 `frontend/dist` 的绝对路径后重新启动。同源部署和使用模拟 API 的浏览器测试应在不设置生产 `VITE_API_BASE_URL` 覆盖的情况下构建。Vite 环境值会编入产物，修改 Hub 环境无法改变已构建的前端。

## 验证

除命令自身切换目录外，下列命令均在仓库根目录运行。可执行定义以[前端脚本](../frontend/package.json)、[CI](../.github/workflows/ci.yml)和[测试清单](../tests/coverage.json)为准。

| 改动 | 本地检查 |
| --- | --- |
| Go 行为 | 用 `gofmt -w` 格式化改动文件，在 `backend/` 运行相关包或用例的 `go test`，按需运行 `go vet ./...`、`go build ./...` |
| 数据库、鉴权、迁移或并发 | 配置隔离的 `TEST_DATABASE_URL`，运行相关测试及 M1/M2/M4 门禁；并发改动使用 `go test -race` |
| 前端逻辑或 API 解码 | `(cd frontend && npm run lint && npm test && npm run build)` |
| 用户交互、终端、文件或设置 | 用 `(cd frontend && npx playwright install --with-deps chromium)` 安装 Chromium；运行 `(cd frontend && npm run test:ui)` 或对应的已记录浏览器套件 |
| Agent 产物或监控 | 构建并验证 Agent，运行相关 M3/M4 检查；原生 Windows 生命周期改动还需要 M5 |
| 翻译文本 | `node frontend/tests/i18n-locale.test.mjs`；人工核对两种语言的含义和插值变量 |
| 文档 | `git diff --check`、相对链接检查、`node tests/render-coverage.mjs --check`、`bash tests/static/adapters-removed.sh --repo` |
| 发布自动化 | `node --test tests/release-policy.test.mjs` |

CI 还使用 [backend/.golangci.yml](../backend/.golangci.yml) 运行 golangci-lint，工具版本固定在工作流中。本地定向检查不能替代必需验收，应明确报告缺失的前提和跳过的测试。部分 Go 测试在无数据库时会跳过；进程退出成功并不代表通过统一验收门禁。

### 隔离数据库测试

在开发容器中单独创建测试库，只需创建一次：

```bash
docker exec talus-dev-db createdb -U talus_dev talus_test
export TEST_DATABASE_URL='postgres://talus_dev:talus_dev_local@127.0.0.1:54329/talus_test?sslmode=disable'
bash tests/run.sh --list
bash tests/run.sh --gate M1
```

先安装前端依赖。测试账号需要 `CREATEDB` 和安装 TimescaleDB 扩展的权限；启动测试创建独立临时数据库，其他测试使用临时 schema。不要指向生产库。M3 还需要镜像构建能力，包括 arm64 执行或模拟；浏览器门禁需要 Chromium。选择 M1–M5 或套件前阅读 [tests/README.md](../tests/README.md)。执行记录写入 `tests/.artifacts/`；`tests/coverage.md` 描述已经实现的覆盖，不代表某次运行通过。

## 修改流程

1. 从最新 `main` 创建工作分支，根据[架构地图](architecture.zh-CN.md)检查相关路径，保留无关的本地工作。
2. API 改动应检查路由与权限、handler、service、repository，再同步前端解码、类型及受影响的 Talus skill 契约。设置项改动应同时检查白名单、运行时快照、UI、部署映射及配置文档。
3. 产品文案同时维护两份语言文件。复用共享 API 客户端及查询、会话处理，保留结构化错误原因和账号切换清理。
4. 为变更的契约与失败路径增补有意义的行为验证。验收变更登记到 `tests/coverage.json`，再运行 `node tests/render-coverage.mjs` 生成报告，不手改生成 Markdown。
5. 同步受影响的中英文文档并执行选定检查，复核 diff 中是否含秘密值、生成产物或无关改动。
6. PR 说明问题、变更后的行为、兼容性影响及实际运行的检查，中文或英文一种语言即可。可以选填模板中对应语言的用户发布说明和升级步骤；CI 会将已有内容汇入 Release 草稿，由维护者在发布阶段补齐翻译。版本标签、区块格式和发布遵循独立的[发布指南](releasing.zh-CN.md#pr-中的发布说明)。

## 文档归属

README 负责产品入门，本文负责开发环境，架构文档负责职责、数据流和需要长期保持的安全约定，测试和发布指南负责各自流程。其他位置引用这些说明，避免复制维护清单。命令、默认值、支持平台须对照脚本和实现核实。已完成的实施计划保留在 PR/Git 历史中，长期有效的决策归入维护中的指南。PR 保存发布说明素材，GitHub Release 草稿负责发布前的汇总和审核。

中英文页面应涵盖相同流程、命令和限制，并互相链接。语言测试只校验翻译键，不检查 Markdown 翻译完整性或事实正确性。CI 目前通过路径过滤跳过纯文档改动，尚无自动的双语正文或通用 Markdown 链接检查；这些检查需在本地完成，不能描述为已有 CI 能力。

## 常见启动问题

| 症状 | 检查项 |
| --- | --- |
| Agent manifest 或程序校验失败 | 重新构建三个平台制品，检查绝对目录和版本一致性 |
| TimescaleDB 扩展或迁移失败 | 使用 TimescaleDB 镜像和合适权限的账号，查看 Hub 日志 |
| 浏览器 API 请求返回 Vite HTML 或 404 | 为 Vite 设置 Hub 基础地址并重启 |
| 已有凭据无法解密 | 恢复该数据库原始主密钥，生成新密钥无法恢复数据 |
| 环境变量修改未生效 | 检查数据库覆盖值，并重启或重建对应进程、容器 |
| 监控未配置或不可达 | 检查节点可达的 Hub 地址、主机指纹确认和平台前提；HTTP 健康不等于 SSH 或 Agent 上报正常 |
