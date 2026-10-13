# JWT 认证延迟基准报告（Phase 3 前置）

- 依据：`docs/REQUIREMENTS.zh-CN.md` §5.3（正常延迟目标）与 `docs/DEVELOPMENT-PLAN.zh-CN.md` T1.5 DoD（"基准测量报告产出"）。
- 结论：**达标并通过阈值冻结** —— gate 等待 + DB 连接池等待 + 版本查询合计 **p95 = 2.44 ms ≤ 20 ms、p99 = 3.17 ms ≤ 50 ms**（25 req/s、8 并发、预热 30 s 后实测 5 分钟，7500 样本、0 错误）。
- 附带发现（非本目标范围）：`/api/v1/servers/summary` 端点自身的可见延迟约 55 ms，成本几乎全部来自 metrics 最近值查询（约 17 ms/节点），与 JWT 准入无关，另列改进项。

## 1. 测试环境（实际值）

| 项 | 值 |
| --- | --- |
| 主机 | tx（Talus server_id=8），4 vCPU / 15484 MiB，Linux + SSD |
| Hub | 容器 `compose-quantify-optical-bandwidth-m9qml4-hub-1`，镜像 `talus-local:v0.9.0-test.4`（构建自 `d6d8a5a`），无 CPU/内存限制 |
| 数据库 | 同机容器 `...-db-1`，`timescale/timescaledb:latest-pg16`（PostgreSQL 16.15 + TimescaleDB 2.30.2） |
| RTT | Hub 与主库同机，经容器网络，实测 `/api/v1/version` p95 = 1.43 ms（客户端/HTTP 底噪） |
| 连接池 | compose 未设置 `DB_MAX_OPEN_CONNECTIONS` → 应用默认 **32**（`SetMaxIdleConns`=16）；PostgreSQL `max_connections`=100，`shared_buffers`=3871 MB |
| 负载客户端 | tx 本机 `python3` 多线程（loopback HTTP，无 TLS），单调时钟逐请求计时 |
| 账号 | 一次性账号（本次测量后即删除），经 `POST /api/v1/auth/login` 换取有效 JWT |

## 2. 测量方法

- 负载：**25 req/s**、**最多 8 并发**（有界队列 + 起搏发送），**预热 30 s**、**实测 300 s**，共 7500 个样本。
- 分位：nearest-rank（`p` 分位取排序后第 `round(p·n+0.5)-1` 个），单位 ms。
- 被测路径（对应 §5.3 的"JWT 准入的 gate 等待、DB 连接池等待和版本查询"合计）：
  `middleware.Auth`（HS256 校验）→ 每用户 gate（取锁 + **主库读 `users.token_version`**）→ 命中一个不存在的资源返回 404。
  该 404 路由**不做任何业务查询**，因此测量值即"认证端点"成本；未复用缓存版本，符合"不能通过缓存版本达标"的要求。
- 进程重启后的首次准入走 `LockedTokenVersion`（`SELECT … FOR UPDATE`）属冷路径，不计入稳态分位；预热 30 s 已覆盖首个请求。

## 3. 结果

### 3.1 目标（gate 等待 + 池等待 + 版本查询）

| 指标 | 实测 | 目标 | 判定 |
| --- | ---: | ---: | --- |
| p50 | 2.10 ms | — | — |
| p90 | 2.30 ms | — | — |
| **p95** | **2.44 ms** | ≤ 20 ms | ✅ 8× 余量 |
| **p99** | **3.17 ms** | ≤ 50 ms | ✅ 16× 余量 |
| p99.9 | 5.05 ms | — | — |
| max / mean | 25.13 ms / 2.15 ms | — | 单次尾部尖峰，无错误 |

条件：7500/7500 成功（全部 404，即"认证通过、资源不存在"），达成 25.0 req/s，0 个非预期状态码。

### 3.2 分解测量（同环境、25 req/s/8 并发或 1 req/s/1 并发）

| 路径 | 并发 | p50 | p95 | 说明 |
| --- | --- | ---: | ---: | --- |
| `/api/v1/version` | 25/8 | 1.26 ms | 1.43 ms | 无鉴权、无 DB：客户端 + HTTP + Hub 底噪 |
| `/api/v1/servers/summary`（无凭据 401） | 1/1 | 1.36 ms | 1.57 ms | 鉴权拒绝，不进入 gate/业务 |
| `/api/v1/servers/999999`（有效 JWT） | 1/1 | 2.11 ms | 4.57 ms | gate + 版本读，无业务查询 |
| `/api/v1/servers/1`（有效 JWT） | 1/1 | 17.12 ms | 28.79 ms | 单节点详情（含 metrics 最近值查询） |
| `/api/v1/servers/summary`（有效 JWT） | 1/1 | 54.99 ms | 60.35 ms | 3 节点列表；成本随节点数近似线性 |
| `/api/v1/servers/summary`（有效 JWT） | 25/8 | 67.29 ms | 85.15 ms | 同一端点在满负载下（p99 96.23 ms、7500 样本、0 错误） |

结论：**认证成本本身是毫秒级常数**（1 rps 与 25 rps 下均为 ~2 ms），而 `/servers/*` 的可见延迟由 metrics 最近值查询主导（约 17 ms/节点）。

## 4. 阈值冻结

按 §5.3 的目标冻结，数值不变：

- **gate 等待 + DB 连接池等待 + 版本查询合计：p95 ≤ 20 ms、p99 ≤ 50 ms**（本次实测 2.44 / 3.17 ms）。
- 基准条件保持 §5.3 原文：Linux/SSD、≥4 vCPU/8 GiB、单 Hub 与主库同机或 RTT ≤ 2 ms、预热 30 s 后持续 5 分钟、同账号有效 JWT 25 req/s、最多 8 并发、认证端点无其他 DB 业务查询。
- 复测要求：更换镜像/Hub 版本、连接池配置、采集端点数或数据库规格后需重新测量；被测路径需保持"无业务查询"，否则会混入列表查询成本（见 §3.2 最后两行）。

## 5. 改进项（非本目标，另行排期）

1. **`/servers/summary` 的 metrics 最近值查询**：3 节点时约 55 ms、满负载 p95 85 ms，成本近似按节点数线性增长。建议后续用 `(server_id, time DESC)` 索引逐节点取最近一行，或维护独立 `latest_metrics` 表，避免对 metrics 做逐节点扫描。
2. 容器未设置 CPU/内存配额（`NanoCpus/Memory = 0`）：同机其它 20+ 容器会与 Hub/DB 争抢，如需更稳的尾部延迟可加配额。

## 6. 局限与说明

- 背景负载：测量期间 Monitor 正常运行（3 个受管节点 → 约 3 次采集/分钟；3 分钟内写入 6 行 metrics），相对 7500 次认证请求可忽略。
- 客户端与 Hub 同机 loopback，未包含真实网络的 RTT/TLS 成本；§5.3 的基准条件即"同机或 RTT ≤ 2 ms"，与本环境一致。
- 本次仅测单 Hub + 单主库；未覆盖多 Hub 或只读副本场景。
- 一次性账号在测量结束后已从数据库中删除，未遗留凭据。
