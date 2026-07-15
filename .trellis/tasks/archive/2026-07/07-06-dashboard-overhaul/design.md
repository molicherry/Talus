# Beszel 风格仪表盘 + 服务器详情重构 — 技术设计

> 架构、组件树、数据流、路由变更、API 协议。

---

## 1. 组件树

```
App
├── LoginPage (不变)
├── SetupPage (不变)
├── MainLayout (不变结构)
│   ├── Sidebar (不变)
│   ├── Header (不变)
│   └── <Outlet>
│       │
│       ├── DashboardPage (NEW — 替换占位)
│       │   └── DashboardGrid
│       │       ├── DashboardEmpty (无服务器时)
│       │       └── ServerCard[] (when data)
│       │           ├── StatusIndicator (shared)
│       │           ├── MetricBar × 3 (CPU/Mem/Disk)
│       │           └── ActionMenu (编辑/删除)
│       │
│       ├── ServerListPage (改造 — 保留为管理视图)
│       │   └── ServerList (添加 StatusIndicator 列)
│       │
│       ├── ServerDetailPage (HEAVILY 改造)
│       │   ├── ServerInfoBar (NEW)
│       │   │   ├── StatusIndicator (shared)
│       │   │   ├── SystemInfo (OS, CPU, Mem total)
│       │   │   └── ActionButtons (编辑/终端/执行命令)
│       │   ├── MonitoringDashboard (从 /monitoring 搬入)
│       │   │   ├── TimeRangeSelector
│       │   │   ├── CpuChart
│       │   │   ├── MemoryChart
│       │   │   └── DiskChart
│       │   └── LoadingSkeleton / EmptyState / ErrorState
│       │
│       ├── TerminalPage (不变，详情页提供入口)
│       ├── ExecPage (不变，详情页提供入口)
│       └── CredentialPages (不变)
│
└── NotFoundPage (不变)
```

---

## 2. 路由变更

### 当前路由表

| Route | Component | 变更 |
|-------|-----------|:---:|
| `/` | DashboardPage (占位) | **重写** |
| `/servers` | ServerListPage | 改造（加状态列） |
| `/servers/:id` | ServerDetailPage | **大幅改造** |
| `/servers/:id/monitoring` | MonitoringPage | → **重定向** `/servers/:id` |
| `/servers/:id/terminal` | TerminalPage | 不变 |
| `/servers/:id/exec` | ExecPage | 不变 |

### 实现方式

```typescript
// router.tsx 改动
<Route path="/" element={<DashboardPage />} />
<Route path="/servers" element={<ServerListPage />} />
<Route path="/servers/:id" element={<ServerDetailPage />} />
<Route path="/servers/:id/monitoring" element={<Navigate to="/servers/:id" replace />} />
// terminal, exec, credentials ... 不变
```

---

## 3. 数据流

### 3.1 仪表盘数据流

```
DashboardPage
    │
    └── useDashboardData()         ← TanStack Query hook
            │
            └── GET /api/v1/servers   ← 需后端新增 status + latest_metrics 字段
                    │
                    ▼
            Server[] with { status, latest_metrics }
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
    ServerCard  ServerCard  ServerCard ...
        │
        ├── StatusIndicator  ← status 字段
        ├── MetricBar(CPU)   ← latest_metrics.cpu_percent
        ├── MetricBar(Mem)   ← latest_metrics.memory_percent
        └── MetricBar(Disk)  ← latest_metrics.disk_percent
```

**TanStack Query 配置**:
```typescript
// features/dashboard/hooks/use-dashboard.ts
export function useDashboardData() {
  return useQuery({
    queryKey: ["dashboard"],
    queryFn: getServersWithMetrics,  // 调用 GET /api/v1/servers
    refetchInterval: 60_000,          // 60s 自动刷新
    staleTime: 30_000,
  });
}
```

### 3.2 服务器详情数据流

```
ServerDetailPage
    │
    ├── useServer(id)              ← GET /api/v1/servers/{id}
    │       └── ServerInfoBar      ← name, host, OS info, status
    │
    └── useMetrics(id, timeRange)  ← GET /api/v1/metrics/{id}?from=&to=&interval=
            └── MonitoringDashboard
                    ├── CpuChart
                    ├── MemoryChart
                    └── DiskChart
```

### 3.3 后备方案（前端并行查询）

如果后端短期内无法在 `GET /api/v1/servers` 中添加 `status` + `latest_metrics`：

```typescript
// 前端并行获取服务器列表 + 各服务器状态
export function useDashboardData() {
  const serversQuery = useQuery({
    queryKey: ["servers"],
    queryFn: getServers,
  });

  const statusesQuery = useQueries({
    queries: (serversQuery.data ?? []).map((s) => ({
      queryKey: ["servers", s.id, "status"],
      queryFn: () => getServerStatus(s.id),
    })),
    enabled: !!serversQuery.data?.length,
  });

  // merge servers + statuses → enriched servers
  const enriched = useMemo(() => {
    if (!serversQuery.data) return [];
    return serversQuery.data.map((server, i) => ({
      ...server,
      status: statusesQuery[i]?.data?.status ?? "unknown",
      lastSeen: statusesQuery[i]?.data?.last_seen,
    }));
  }, [serversQuery.data, statusesQuery]);

  return { data: enriched, isLoading: serversQuery.isLoading, ... };
}
```

**注意**: N+1 查询在 ≤15 台服务器场景下可接受（15 个并行 HTTP 请求，浏览器连接池可处理）。但推荐方案 A（后端聚合）。

---

## 4. API 协议

### 方案 A（推荐）：扩展现有 Server API

```diff
GET /api/v1/servers 响应:
{
  "data": [
    {
      "id": 1,
      "name": "web-01",
      "host": "10.0.0.1",
      "port": 22,
      "description": "production web",
+     "status": "online",
+     "last_seen": "2026-07-06T10:30:00Z",
+     "latest_metrics": {
+       "cpu_percent": 12.5,
+       "memory_percent": 45.2,
+       "disk_percent": 78.1
+     },
      "owner_id": 1,
      "created_at": "...",
      "updated_at": "..."
    }
  ]
}
```

### 需求：后端需新增的字段

| 字段 | 类型 | 来源 | 说明 |
|------|------|------|------|
| `status` | `"online" \| "offline" \| "checking" \| "unknown"` | SSH 连通性 + 最近指标时间 | `GET /api/v1/servers/{id}/status` 已有类似逻辑 |
| `last_seen` | `ISO 8601 string` | 最近一次成功采集时间 | — |
| `latest_metrics` | `{ cpu_percent, memory_percent, disk_percent }` | metrics 表最新一行 | 仪表盘卡片显示用 |

### 现有 GET /api/v1/servers/{id}/status 不变

单独获取单台服务器状态的能力保留，供 ServerDetailPage 实时刷新使用。

---

## 5. 共享组件设计

### 5.1 StatusIndicator

```typescript
// components/ui/status-indicator.tsx
interface StatusIndicatorProps {
  status: "online" | "offline" | "checking" | "unknown";
  showPulse?: boolean;      // default: status === "online" || status === "checking"
  size?: "sm" | "md";       // default: "md"
  className?: string;
}
```

**实现要点**:
- `online` + `checking`: 显示 `<span>` + 外层 `<span className="animate-ping">`（CSS 脉冲）
- `offline` + `unknown`: 纯色点，无动画
- 使用 Tailwind `rounded-full` + `bg-{color}-500`
- `sr-only` 文本描述状态供屏幕阅读器

### 5.2 MetricBar

```typescript
// components/ui/metric-bar.tsx
interface MetricBarProps {
  label: string;           // "CPU", "Memory", "Disk"
  value: number | null;    // 0-100，null 表示无数据
  icon?: LucideIcon;       // Cpu, MemoryStick, HardDrive
  className?: string;
}
```

**实现要点**:
- 进度条背景：`bg-gray-200 dark:bg-gray-700` 全宽
- 填充色自动计算：`value < 65 ? "bg-green-500" : value < 90 ? "bg-yellow-500" : "bg-red-500"`
- 右侧百分比文字：右对齐，等宽字体 (`font-mono tabular-nums`)
- 无数据时：灰色 + "N/A"

---

## 6. 文件结构

```
frontend/src/
├── features/
│   └── dashboard/                         # NEW 模块
│       ├── api.ts                         # getServersWithMetrics()
│       ├── hooks/
│       │   └── use-dashboard.ts           # useDashboardData()
│       └── components/
│           ├── dashboard-page.tsx          # Page (route-level)
│           ├── dashboard-grid.tsx          # Grid + loading/empty/error
│           ├── server-card.tsx             # 单张卡片
│           ├── server-card-skeleton.tsx    # 骨架屏
│           └── dashboard-empty.tsx         # 空状态
│
├── components/
│   └── ui/
│       ├── status-indicator.tsx            # NEW (shared across features)
│       └── metric-bar.tsx                 # NEW (shared across features)
│
├── features/
│   ├── servers/
│   │   └── components/
│   │       ├── server-detail-page.tsx      # HEAVILY MODIFIED
│   │       ├── server-info-bar.tsx         # NEW
│   │       └── server-list.tsx            # MODIFIED (add status col)
│   │
│   └── monitoring/                        # 保留（组件被 detail page 引用）
│       └── components/
│           ├── monitoring-dashboard.tsx     # 搬到 features/servers 或保留引用
│           ├── cpu-chart.tsx               # 不变
│           ├── memory-chart.tsx            # 不变
│           ├── disk-chart.tsx              # 不变
│           └── time-range-selector.tsx     # 不变
│
├── types/
│   └── models.ts                          # MODIFIED: ServerSchema 新增字段
│
└── app/
    └── router.tsx                         # MODIFIED: 新增 redirect, 替换 DashboardPage
```

---

## 7. 状态管理

遵循现有规范（`state-management.md`）：无全局 store。所有数据通过 TanStack Query 管理。

| 数据 | 位置 | 模式 |
|------|------|------|
| 服务器列表 + 状态 | `useDashboardData()` | `queryKey: ["dashboard"]`，60s refetch |
| 单服务器详情 | `useServer(id)` | `queryKey: ["servers", id]` |
| 监控指标时序 | `useMetrics(id, range)` | `queryKey: ["metrics", id, range]`，60s refetch |
| 时间范围 | React `useState` in MonitoringDashboard | 组件本地状态 |
| 搜索过滤文本 | React `useState` in DashboardPage | 组件本地状态 |

---

## 8. 响应式断点

| 断点 | 仪表盘网格 | 详情页图表 |
|------|-----------|-----------|
| `lg` (≥1024px) | 3 列 | 2 列 |
| `sm` (≥640px) | 2 列 | 1 列 |
| `<640px` | 1 列 | 1 列 |

---

## 9. 与现有代码的关系

### 复用（不改动）
- `features/monitoring/` 所有图表组件（cpu-chart, memory-chart, disk-chart, time-range-selector, loading-skeleton, empty-state, error-state）
- `features/monitoring/hooks/use-metrics.ts`
- `features/monitoring/api.ts`
- `components/layout/` 全部
- `features/terminal/` 全部
- `features/credentials/` 全部

### 改造
| 文件 | 改动 |
|------|------|
| `app/router.tsx` | `/` → DashboardPage, `/monitoring` → redirect |
| `features/servers/components/server-detail-page.tsx` | 嵌入 InfoBar + MonitoringDashboard |
| `features/servers/components/server-list.tsx` | 表格添加状态列 |
| `types/models.ts` | ServerSchema 新增 status, latest_metrics |

### 新建
| 文件 | 说明 |
|------|------|
| `features/dashboard/` | 整个模块（5 个文件） |
| `components/ui/status-indicator.tsx` | 状态点 |
| `components/ui/metric-bar.tsx` | 进度条 |
| `features/servers/components/server-info-bar.tsx` | 详情页信息栏 |

### 可删除（redirect 后）
- `features/monitoring/components/monitoring-page.tsx` — 路由入口不再需要

---

## 10. 技术决策

| 决策 | 选择 | 理由 |
|------|------|------|
| 后端数据聚合位置 | 扩展现有 Server API（方案 A） | 1 次请求 vs N+1 次，减少前端复杂度 |
| 监控图表位置 | 嵌入服务器详情页（`/servers/:id`） | 减少独立页面跳转，Beszel 模式 |
| 终端/命令执行入口 | 保留独立路由 | URL 直接访问有价值，详情页仅提供跳转入口 |
| 仪表盘视图 | 仅卡片网格（不实现 Table 切换） | ≤15 台服务器，卡片视图已足够；Table 在 `/servers` 已有 |
| 共享组件 | `components/ui/` 而非 feature 内部 | StatusIndicator 和 MetricBar 在 Dashboard + Detail 两处使用 |
| 状态管理 | 纯 TanStack Query | 符合项目规范，无额外全局 store |
