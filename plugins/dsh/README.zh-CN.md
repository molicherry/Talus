# Talus 的 DeepSeek Harness 插件

[English](README.md) | 简体中文

`dsh-plugin-talus` 0.1.0 为 DeepSeek Harness（dsh）打包了完整的
[Talus skill](skills/talus/SKILL.md)。插件通过 dsh 官方文件系统技能提供器加载
服务器管理、SSH 命令、监控、SFTP 和服务代理调用指南。skill 及参考文档均随包提供，
安装后不依赖 Talus 源码仓库。

## 环境要求

- Node.js 24.9.0 或更高版本，以及 DSH 0.2.2-rc.1。包内 `engines.dsh` 范围为
  `~0.2.2-rc.1`；dsh 插件 API 仍处于预览阶段。
- 从 dsh 执行环境可以访问的运行中 Talus 实例。
- 具备任务所需作用域的 Talus API 密钥，以及 dsh 可调用的 `curl` 或等效 HTTP
  工具。权限说明见随包提供的[权限指南](skills/talus/SKILL.md#authentication-and-permissions)。

## 配置执行环境

将 `TALUS_URL` 设为实例基础地址，不包含 `/api/v1`。未设置时 skill 使用
`http://localhost:8080`；其中 `localhost` 指运行 dsh 的环境。
在 **Talus Web UI → API Keys** 获取 `TALUS_API_KEY`。通过 Bash 启动时可这样设置：

```bash
export TALUS_URL='https://talus.example.com'
read -r -s -p 'Talus API key: ' TALUS_API_KEY
printf '\n'
export TALUS_API_KEY
```

从同一环境启动 dsh，使执行请求的工具继承这些变量。需要持久配置，或使用 Desktop、
远程执行时，请在实际执行请求的进程环境或密钥设置中配置，再重启进程。
已运行的 Desktop 应用不会继承之后在终端导出的变量。Talus 服务端部署的 `.env`
不会自动配置 dsh。不要将 API 密钥写入插件文件或粘贴到聊天中。

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

确认可用技能中包含 `talus`，然后输入：

> 使用 talus skill 和已配置的环境变量，通过真实 API 请求列出我的服务器，报告名称和
> 状态，不要显示 API 密钥。

此任务需要 `servers:read`。仅用于服务访问的密钥，可以改为要求列出已注册服务并读取
所选服务的使用指南。安装成功、技能加载成功本身不代表已验证真实 Talus 连接。

## 更新或卸载

更新前完全停止目标 profile。先移除已有插件，再添加更新后的目录，最后重启 profile：

```bash
dsh plugin --profile web remove dsh-plugin-talus
dsh plugin --profile web add ./plugins/dsh
```

以上路径以 Talus 仓库根目录为工作目录；独立副本请使用绝对路径。将命令中的 `web`
统一替换为实际 profile。卸载时只执行 `remove` 命令。实例配置仍保存在执行环境中。

## 维护和分发

仓库的 `skills/talus/` 是维护源。在 Talus 源码仓库内进入本插件目录，更新随包副本并
执行检查：

```bash
npm ci
npm run sync-skills
npm run check-skills
npm test
npm pack
```

`npm test` 使用 Cordis 和 dsh 的真实文件系统技能提供器验证注册与技能加载，
不连接 Talus 实例。`npm pack` 生成 `dsh-plugin-talus-0.1.0.tgz`；此包尚未发布到 npm。
可以直接安装压缩包（将示例路径替换为实际绝对路径）：

```bash
dsh plugin --profile web add /absolute/path/dsh-plugin-talus-0.1.0.tgz
```

也可以解压后，用 `package/` 目录的绝对路径安装。
包内包含 skill、[文件操作参考](skills/talus/references/files.md)和
[监控参考](skills/talus/references/metrics.md)。

技能提供器只加载包内的 `skills/` 目录，不监听文件变化。更新 skill 后需同步并重新安装
完整插件，再重启 profile 加载新文件。
