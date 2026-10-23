# Windows Server 监控

Talus 提供 Windows amd64 原生 Agent。添加服务器后，Hub 通过 OpenSSH/SFTP
自动安装 Agent；Agent 作为 Windows 服务开机启动，主动通过 WSS/HTTPS 上报。
正常采集不占用 SSH 会话，也不要求在 Windows 上开放额外的监控端口。

## 支持范围

- 首版目标：Windows Server 2019、2022、2025 x64；原生 CI 覆盖 2022、2025。
  2019 仍需在实际环境中验收。Windows ARM64、32 位系统不在首版范围。
- CPU、内存、分页文件、磁盘容量、磁盘读写、网络流量、系统版本和运行时间。
- Linux 的 1/5/15 分钟负载均值在 Windows 上显示“不适用”，不写入虚假的 0。
  某项指标读取失败时保留其他指标，并报告该项错误。
- IIS、SQL Server、Windows 服务状态不在本次采集范围。
- 本文描述监控 Agent。浏览器终端、文件浏览器和命令执行对 Windows 的完整
  兼容性不由本次监控实现保证。部署支持 OpenSSH 默认 shell 为 cmd.exe 或 PowerShell。

## 在哪里配置

| 配置 | 位置 | 要求 |
| --- | --- | --- |
| OpenSSH Server、SFTP | Windows Server | 启动 `sshd`，允许 Hub 的 SSH 连接，启用 SFTP 子系统 |
| SSH 账号 | Talus → 凭证 | 使用具有本机管理员权限的账号；Talus 不会交互式提权 |
| 主机、SSH 端口、凭据、连接路线 | Talus → 服务器 → 添加/编辑 | 支持现有直连、SSH 跳板和 SOCKS5 路线 |
| SSH 主机密钥 | Talus 主机密钥验证流程 | 验证服务器指纹后信任 |
| Hub 地址 | Talus → 系统设置 → 监控 | 填写 Windows 节点可访问的 HTTPS 地址；也可部署时设置 `AGENT_HUB_URL` |
| 采集间隔 | Talus → 系统设置 → 监控 | 与 Linux Agent 使用同一配置规则 |
| PowerShell、任务计划程序 | Windows Server | Windows PowerShell 5.1+；`Schedule` 服务运行中 |
| HTTPS 信任 | Windows 本机证书存储 | Hub 证书须受信任；私有 CA 应安装到本地计算机的受信任根存储，供 SYSTEM 服务读取 |

配置完成后等待监控状态变为“就绪/在线”。节点的出站连接需要同时支持
HTTPS 和 WebSocket，不能只允许交互式 SSH 账号访问 Hub。
若 WSS 不可用，Agent 会尝试 HTTPS 上报。

## 安装、升级和删除

- 服务名：`Talus-Agent-<安装 ID>`，以 LocalSystem 运行，自动启动，并配置故障重启。
- 文件目录：`%ProgramData%\Talus-Agent\<安装 ID>`。配置包含节点专用令牌；目录
  ACL 仅授权 SYSTEM 和本机 Administrators，不依赖 Windows SFTP 的 POSIX chmod。
- 首次安装、配置更新和版本升级都先校验 SHA-256。一次性计划任务完成服务切换，
  等待新程序与 Hub 完成认证；失败时恢复上次程序和配置。SSH 断开不会终止该任务。
- 本地待发送队列沿用 24 小时 / 64 MiB 上限。Windows 文件锁防止两个进程同时
  使用队列；内核启动 GUID 区分系统重启，避免跨重启计算流量尖峰。
- 删除 Talus 服务器后，由独立的本机清理任务停止并删除对应 Windows 服务、相关部署任务和安装目录。
  若有升级正在运行，清理任务会等待部署锁后执行，不受删除请求的 SSH 超时影响。
  更换 SSH 管理员账号不会改变安装位置。删除仍需要管理员权限。若 SSH 已不可达，Hub 会撤销节点凭据，但本机文件和服务需要恢复连接后手动清理。
- 部署锁文件保留在安装目录外，避免正在等待的部署和删除操作使用不同的锁。

## 故障排查

| 现象 | 检查 |
| --- | --- |
| 平台不支持 | 是否为 x64 Windows，SSH 默认 shell 能否执行 `powershell.exe` |
| Windows 服务管理不可用 | SSH 登录是否具有管理员权限；PowerShell、任务计划程序是否可用；ProgramData 是否为可写的本地目录 |
| 上传失败 | SFTP 子系统是否启用，SSH 路线是否能传文件 |
| 校验失败 | 上传是否被安全软件拦截或修改；不要跳过哈希校验 |
| 健康检查失败 | Windows 服务能否访问配置的 Hub HTTPS 地址、证书链是否受本机信任、反向代理是否允许 Agent 路径 |
| 单项指标错误 | 性能计数器、磁盘访问或系统 API 是否可用；不会用 0 代替失败的采集值 |

## 开发验证

`backend/build/agents.sh` 同时产出 Linux amd64/arm64 和 Windows amd64 文件；Hub
启动时检查全部清单、大小、哈希和 ELF/PE 架构，缺少 Windows 文件也会失败。

Windows 管理员终端中可运行：

```powershell
cd backend
$env:TALUS_WINDOWS_INTEGRATION='1'
go test -v -count=1 ./internal/agentclient ./internal/agentwire ./internal/agentwindows
```

原生验收会建立临时服务和计划任务，实际运行 Agent，测试采集、配置升级、
不可达 Hub 时的回滚及重复卸载，结束后清理。Linux 下的交叉编译和 SSH 模拟测试
不能代替这组 Windows 验收。
