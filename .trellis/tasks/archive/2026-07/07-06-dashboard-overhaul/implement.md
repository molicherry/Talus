# Beszel 风格仪表盘 + 服务器详情重构 — 执行计划

> 分阶段实施，每阶段独立可验证。优先前端，后端 API 确认后并行推进。

---

## 阶段 1：共享基础组件（前端）

### 1.1 StatusIndicator 组件
- [ ] 创建 `components/ui/status-indicator.tsx`
- [ ] 实现 4 种状态：online (green+pulse), offline (red), checking (yellow+pulse), unknown (gray)
- [ ] 支持 size prop（sm/md），className 透传
- [ ] sr-only 文本
- [ ] `lsp_diagnostics` 通过

### 1.2 MetricBar 组件
- [ ] 创建 `components/ui/metric-bar.tsx`
- [ ] 进度条 + 百分比文字布局
- [ ] 自动颜色计算（绿/黄/红/灰）
- [ ] 支持 icon prop，label 显示
- [ ] null value → "N/A" 灰色
- [ ] `lsp_diagnostics` 通过

**验证**: 组件可渲染，不同 props 组合外观正确

---

## 阶段 2：仪表盘首页（前端）

### 2.1 Dashboard 模块骨架
- [ ] 创建 `features/dashboard/` 目录结构
- [ ] `features/dashboard/api.ts` — `getServersWithMetrics()` API 函数
- [ ] `features/dashboard/hooks/use-dashboard.ts` — TanStack Query hook (60s refetch)
- [ ] `features/dashboard/components/dashboard-page.tsx` — 页面级组件
- [ ] `features/dashboard/components/dashboard-grid.tsx` — 网格容器 + loading/empty/error 三态

### 2.2 ServerCard 组件
- [ ] `features/dashboard/components/server-card.tsx`
- [ ] 卡片布局：StatusIndicator + 名称 + ActionMenu（P1 操作菜单）
- [ ] 三行 MetricBar（CPU/Memory/Disk）
- [ ] 点击整卡 `navigate(/servers/:id)`
- [ ] 卡片 hover 效果（shadow/border transition）

### 2.3 状态组件
- [ ] `features/dashboard/components/server-card-skeleton.tsx` — `animate-pulse` 骨架
- [ ] `features/dashboard/components/dashboard-empty.tsx` — 引导空状态 + CTA

### 2.4 路由接入
- [ ] 修改 `app/router.tsx`：`/` 指向新 `DashboardPage`
- [ ] 移除旧的内联 `DashboardPage` 占位函数

**验证**: 登录后首页显示服务器卡片（有/无服务器两种情况）；卡片可点击跳转详情页

---

## 阶段 3：服务器详情页重构（前端）

### 3.1 ServerInfoBar 组件
- [ ] 创建 `features/servers/components/server-info-bar.tsx`
- [ ] 顶部横条：服务器名 + StatusIndicator + OS/运行时间
- [ ] 操作按钮组：编辑、终端、命令执行

### 3.2 监控图表嵌入
- [ ] 修改 `server-detail-page.tsx`：在 InfoBar 下方嵌入 `MonitoringDashboard`
- [ ] 从 `features/monitoring/` 导入图表组件（cpu-chart, memory-chart, disk-chart, time-range-selector）
- [ ] 传入 serverId + timeRange state

### 3.3 路由重定向
- [ ] 在 `router.tsx` 添加 `/servers/:id/monitoring` → `<Navigate to="/servers/:id" replace />`

### 3.4 服务器列表页改造（可选 P1）
- [ ] 修改 `server-list.tsx`：表格添加 StatusIndicator 列

**验证**: 详情页正确渲染 InfoBar + 图表；`/servers/:id/monitoring` 自动跳转；时间范围切换正常

---

## 阶段 4：后端 API 适配（前后端协作）

### 4.1 TypeScript 类型更新
- [ ] 修改 `types/models.ts`：ServerSchema 添加 `status`, `last_seen`, `latest_metrics` 可选字段
- [ ] 类型向后兼容（字段 optional）

### 4.2 后端 API 改动（需后端工程师配合）
- [ ] `GET /api/v1/servers` 返回 `status` 字段（复用现有 status check 逻辑）
- [ ] `GET /api/v1/servers` 返回 `last_seen` 字段（metrics 表最近 timestamp）
- [ ] `GET /api/v1/servers` 返回 `latest_metrics` 对象（CPU/Mem/Disk 最新值）

### 4.3 后备方案（如后端改动耗时较长）
- [ ] 仪表盘前端使用并行 `useQueries` 方式获取状态
- [ ] `features/dashboard/api.ts` 中实现 status 聚合逻辑

**验证**: Chrome DevTools Network 面板确认仪表盘仅 1 次 API 请求（方案 A）或 ≤N+1 次（方案 B）

---

## 阶段 5：质量保障 & 收尾

### 5.1 类型检查 & 代码质量
- [ ] `lsp_diagnostics` frontend/src/ — 0 errors
- [ ] `npm run lint` 通过
- [ ] `npm run build` 通过（TypeScript + Vite）

### 5.2 国际化
- [ ] 在 `i18n/locales/en.json` + `zh-CN.json` 添加仪表盘相关翻译键
- [ ] 所有硬编码文案替换为 `t()` 调用

### 5.3 手动验证清单
- [ ] 仪表盘空状态 → 添加服务器 → 卡片出现，状态正确
- [ ] 离线服务器卡片正确显示红色状态点
- [ ] 点击卡片 → 详情页，图表正确加载
- [ ] 时间范围切换 → 图表数据更新
- [ ] 暗色模式切换 → 所有新组件正常
- [ ] 响应式测试：1920px / 1024px / 375px 宽度

### 5.4 清理
- [ ] 移除 `features/monitoring/components/monitoring-page.tsx`（入口组件已废弃）
- [ ] 确认无未使用的 import

**验证**: `npm run build` 成功，所有手动验收通过

---

## 依赖关系图

```
阶段 1 (StatusIndicator, MetricBar)
    │
    ▼
阶段 2 (Dashboard Page)  ──────┐
    │                           │
    ▼                           ▼
阶段 3 (Server Detail 重构)  阶段 4 (后端 API)
    │                           │
    └───────────┬───────────────┘
                ▼
         阶段 5 (Quality)
```

- 阶段 2 依赖阶段 1
- 阶段 3 不依赖阶段 2 完成（可并行开发，共享组件已就绪）
- 阶段 4 与阶段 2/3 可并行（前端先用后备方案 mock）
- 阶段 5 依赖所有前置阶段

---

## 回滚点

| 阶段 | 回滚方式 |
|------|------|
| 阶段 1-2 | 删除 `features/dashboard/` + `components/ui/` 新文件 + revert router.tsx |
| 阶段 3 | revert server-detail-page.tsx + 删除 server-info-bar.tsx + revert router.tsx 重定向 |
| 阶段 4 | revert types/models.ts + revert 后端 commit（单独 PR） |
