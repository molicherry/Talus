# 使用日志部署说明

使用日志已覆盖服务器、凭据、服务和 API Key 的资源变更，以及凭据查看、主机密钥信任、SSH 命令、服务转发和交互终端。管理员通过侧栏“使用日志”查看列表和详情；API Key 不允许查询日志。命令、正文、输出、终端内容和解密后的敏感信息不进入日志。

## 首次升级

1. 备份数据库，停止所有旧版本 Talus 实例，并等待旧请求及数据库事务完成。
2. 启动新版本。启动时自动增加使用日志、实例租约、回填进度表，以及安全审计的可空唯一 `operation_id`；新请求开始采集日志。
3. 确认所有旧写入者都已停止并排空后，设置 `USAGE_LOG_LEGACY_WRITERS_DRAINED=true`，重启至少一个新实例以启动历史回填。多实例通过 PostgreSQL advisory lock 保证仅一个执行者。
4. 观察 `legacy usage backfill completed` 日志，或用下面的进度查询确认 `completed_at` 非空，再移除该开关。失败会输出固定原因并保留进度；重启后可续跑。完成标记存在时再次执行直接退出，摘要清理后不会再次从原审计表补回。

该开关默认关闭，避免在旧写入事务仍可能提交时提前宣告历史集合完整。关闭期间，新操作照常采集，旧安全审计原表保留，历史摘要清理暂停。首版采用协调切换，不支持旧写入者与新写入者长期混用。

旧 API Key 的用户归属会自动补齐：升级时无归属的旧 Key 绑定到未删除管理员中 ID 最小的账号，已有非零归属保持不变。升级兼容尚无 `user_id` 列的旧表，以及归属列允许空值的旧表。必要的 schema 事务用 `owner_binding_version=0` 固定既有无归属集合，再把新行默认值设为 1；以后新增的无归属 Key 不会因重启被自动分配。后台每批最多 100 条、单次最多 2 秒、每轮最多 8 批，失败输出固定告警并每分钟重试，无需额外开关。尚无管理员时等待，首次管理员创建提交后会触发后台补齐；绑定失败不回滚初始化或阻止服务启动。并发初始化仍串行处理。Key 原文、密文、作用域、服务器范围及已保存的日志不改写。

历史审计回填的进度查询：

```sql
SELECT id, checkpoint, final_bound, imported, completed_at
FROM usage_log_backfill_states
WHERE id = 'usage-log-audit-legacy-v2';
```

没有记录或 `completed_at IS NULL` 表示尚未完成；仅检查 checkpoint 或 imported 不足以确认最终查漏已经封口。旧 Key 归属待处理数量可以单独检查：

```sql
SELECT COUNT(*) AS pending_legacy_keys
FROM api_keys
WHERE owner_binding_version = 0 AND (user_id = 0 OR user_id IS NULL);
```

历史摘要显示“结果未知”，没有可靠身份来源、真实关闭时间或耗时时不补造这些事实。旧版本曾把 Key ID 当作用户 ID，因此回填不把旧 `user_id` 当作可信用户归属；保留经过限制的历史名称。

## 配置与观察

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `DB_MAX_OPEN_CONNECTIONS` | 32 | 共享连接池上限；低于 12 时按 12 处理 |
| `USAGE_LOG_WRITE_CONCURRENCY` | 4 | 普通写入上限；实际受共享连接池四分之一总额度约束 |
| `USAGE_LOG_ACTIVE_LIMIT` | 4096 | 进程内长操作追踪容量 |
| `USAGE_LOG_RETENTION_DAYS` | 30 | 普通摘要的保留天数 |
| `USAGE_LOG_SENSITIVE_RETENTION_DAYS` | 90 | 敏感查看、删除和主机密钥信任摘要的保留天数 |
| `USAGE_LOG_LEGACY_WRITERS_DRAINED` | false | 已完成旧写入者排空时启用历史回填 |

未设置或无效的整数采用默认值；采集限制按进程计算，新增副本需要重新核算总写入容量。安全审计使用独立槽位及重试队列，使用日志失败不回滚安全审计，也不改变业务结果。

每分钟输出 `usage capture statistics`，包含采集跳过、写入失败、结束重试、对账、安全审计失败/丢失、摘要丢失等计数，以及活跃数、待完成数量/字节数和最老待完成年龄。首次清理和历史封口时输出 `usage retention cleanup eligibility`，按受限来源/action/终态分组报告候选数量；报告失败会推迟相应清理。回填完成日志包含 imported、final_bound 和 completed_at。告警只包含操作 ID、action 和固定原因，限频输出。

失联的运行记录会显示“状态未知（追踪中断）”，不表示真实业务已结束。实例恢复后会根据本地活跃或待完成事实修正；被保留清理退休的旧实例不能复活已删除摘要。

列表默认最近 24 小时，单次时间跨度最多 90 天。翻页采用有界 keyset，不计算总数；时间和筛选在一次查询中冻结。游标 30 分钟过期后需要显式刷新。首页自动检查只提示更新，不移动当前记录。

## 验证

后端集成测试需要独立测试 PostgreSQL，通过 `TEST_DATABASE_URL` 指定；测试创建并删除自己的 schema，不修改原有表。运行 `go test -race ./...`、`go vet ./...` 和仓库配置的 golangci-lint。前端运行 `npm test`、`npm run lint`、`npm run build` 及 `npm run test:ui:built`；浏览器回归已接入 CI，安装和本地执行见 [frontend/tests/README.md](../frontend/tests/README.md)。手机检查覆盖 320、375、390px 的触摸筛选、长字段详情和分页。

故障测试覆盖审计与摘要独立写入、幂等重试、结束失败恢复、租约/退休屏障、历史查漏和清理、分页及微秒精度、跨账号取消、SSH 取消与非零退出、流式转发、终端首因及资源释放。生产容量仍需依据实际实例数和数据库资源调整。
