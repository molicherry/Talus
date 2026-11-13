# Talus 的 DeepSeek Harness 插件

[English](README.md) | 简体中文

`dsh-plugin-talus` 0.1.0 为 DeepSeek Harness（dsh）打包了完整的
[Talus skill](skills/talus/SKILL.md)，并提供宿主请求工具和凭据代理，支持服务器管理、
SSH 命令、监控、SFTP 和服务代理调用。完整 skill、助手脚本及参考文档均随包提供，
安装后不依赖 Talus 源码仓库的其余文件。

## 环境要求

- Node.js 24.9.0 或更高版本，以及 DSH 0.2.2-rc.1。包内 `engines.dsh` 范围为
  `~0.2.2-rc.1`；dsh 插件 API 仍处于预览阶段。
- 从 dsh 宿主可通过 HTTPS 访问的 Talus 实例，证书需被宿主信任；不支持 HTTP 地址
  或跳过证书校验。
- 具备任务所需作用域的 Talus API 密钥，详见随包提供的
  [权限指南](skills/talus/SKILL.md#authentication-and-permissions)。

## 安装

在 Talus 仓库根目录执行，安装到 Web profile：

```bash
dsh plugin --profile web add ./plugins/dsh
```

若使用独立复制的插件目录，将 `./plugins/dsh` 替换为该目录的绝对路径。
第三方插件界面也支持选择本地绝对目录。安装完成后重启目标 profile。

无界面运行使用 `--profile headless`。Desktop 需要先启动一次以初始化 profile，
完全退出后用 `--profile desktop` 安装，再重新启动。安装到 `web` 不会同时安装到
其他 profile。

## 在插件页配置

打开 **插件页 → Talus**，填写 HTTPS 实例基础地址，例如
`https://talus.example.com`，不包含 `/api/v1`，再填写从
**Talus Web UI → API Keys** 获取的 API 密钥并保存。修改对后续请求立即生效，
无需重新安装或重启。插件自动启动宿主代理，无需手动运行独立代理。

密码输入框不会显示已保存的密钥。同一地址下留空表示保留原凭据；选择
**清除已保存的 API key** 并保存可将其移除。凭据绑定所配置的地址：修改目标地址时
需要重新填写该目标的密钥，原密钥不会发送给另一实例。没有密钥环境变量回退。
不要将密钥粘贴到聊天、放入工作区或导出到模型的 shell 环境。

模型通过 `talus_request` 提交必填的 `path`，以及可选的 `method`、`json`、`headers`、
`timeout_ms`。工具返回 `status`、`headers` 和字符串 `body`；文本/JSON 请求与响应体
限制为 1 MiB。默认超时为 30000 毫秒，最多可设为 300000 毫秒。
宿主取得已保存密钥，只向已配置的 HTTPS 目标添加原有 `X-API-Key` 头。
TLS 保护传输中的认证头；代理不跟随重定向。不引入签名协议，也不修改 Talus 后端认证。

所有二进制上传、下载分别使用随包 Node 助手的 `--data-file`、`--output`；宿主工具
不接收本地文件路径。助手通过本地 Unix-domain socket 或 Windows named pipe 连接
代理，也用于较大的 JSON/文本请求或响应。shell 工具只获得
`DSH_TALUS_BROKER`、`DSH_TALUS_HELPER` 路径，插件不会
注入 API 密钥。skill 在每次 shell 调用中将它们映射为 `TALUS_BROKER`、`TALUS_REQUEST`。

## 凭据存储和迁移

密钥通过 dsh credentials 服务保存。旧安装升级后，请在插件表单重新输入一次 API 密钥；
旧配置中的密钥及密钥环境变量均不会被使用。保存时将输入的密钥写入 credentials 服务，
并清除旧配置字段。默认 `credentials-local` 使用仅所属用户可访问的本地文件（Unix 权限
`0600`），不是操作系统加密钥匙串，也不能强隔离同一用户下的进程。本插件避免将密钥
主动下发给模型、shell 环境或命令行参数。如果模型能任意读取该用户的凭据目录，
更强隔离仍需 OS keychain 凭据提供器，或分账号和操作系统权限边界。

已有 HTTP 实例需要先配置 HTTPS，再在插件中保存地址。旧 HTTP 设置可能仍显示以便修改，
但所有请求都会拒绝使用它。`TALUS_URL`、`TALUS_API_KEY`
及直接携带认证头的 `curl` 属于旧外部客户端用法，此插件及 skill 不再使用。
使用 headless profile 时也需配置插件的宿主凭据。Talus 部署 `.env` 不会自动配置插件。

## 验证

确认可用技能中包含 `talus`，然后输入：

> 使用 talus skill 和 talus_request 实际请求并列出我的服务器，报告名称和状态。

此任务需要 `servers:read`。仅用于服务访问的密钥，可以改为要求列出已注册服务并读取
所选服务的使用指南。安装成功、技能加载成功本身不代表已验证真实 Talus 连接。

## 更新或卸载

更新前完全停止目标 profile。先移除已有插件，再添加更新后的目录，最后重启 profile：

```bash
dsh plugin --profile web remove dsh-plugin-talus
dsh plugin --profile web add ./plugins/dsh
```

以上路径以 Talus 仓库根目录为工作目录；独立副本请使用绝对路径。将命令中的 `web`
统一替换为实际 profile。卸载时只执行 `remove` 命令。重新安装后检查插件设置，
必要时重新填写。旧安装需要此更新才能获得宿主代理，并需在表单重新输入一次密钥；
之后修改配置只需保存。

## 维护和分发

仓库的 `skills/talus/` 是维护源。同步脚本复制原文件，并为包内 skill 添加 dsh 配置
说明，通用 skill 源文件保持不变。在 Talus 源码仓库内进入本插件目录，更新随包副本并
执行检查：

```bash
npm ci
npm run sync-skills
npm run check-skills
npm test
npm pack
```

`npm test` 检查插件，不代表已经验证你的 Talus 部署。`npm pack` 生成 `dsh-plugin-talus-0.1.0.tgz`；此包尚未发布到 npm。
可以直接安装压缩包（将示例路径替换为实际绝对路径）：

```bash
dsh plugin --profile web add /absolute/path/dsh-plugin-talus-0.1.0.tgz
```

也可以解压后，用 `package/` 目录的绝对路径安装。
包内包含 skill、[文件操作参考](skills/talus/references/files.md)和
[监控参考](skills/talus/references/metrics.md)。

技能提供器只加载包内的 `skills/` 目录，不监听文件变化。更新 skill 后需同步并重新安装
完整插件，再重启 profile 加载新文件。
