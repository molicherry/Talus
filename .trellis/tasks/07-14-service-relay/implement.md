# 服务中继 — 实施计划

## Phase 1: 后端核心（model → repo → service → handler）

### 1.1 数据模型 + 加密
- [ ] `backend/internal/model/service.go`: Service struct + BaseModel + GORM tags
- [ ] `backend/cmd/server/main.go`: AutoMigrate 加 `&model.Service{}`
- [ ] 验证：启动后 services 表自动创建，columns 含 encrypted_credentials(jsonb)、salt(bytea)

### 1.2 数据访问层
- [ ] `backend/internal/repository/service.go`: ServiceRepo (Create, FindAll, FindByID, FindByServerID)
- [ ] 验证：单元测试 Create + FindByID 往返

### 1.3 业务逻辑层
- [ ] `backend/internal/service/service_relay.go`:
  - `Create(ctx, input)` — 校验 name/base_url/credentials → 生成 salt → Encrypt 逐个 value → 存入
  - `List(ctx, serverID)` — 返回所有服务；若 serverID 不为 nil 则按 server_id 筛选
  - `Relay(ctx, id, relayBody)` — 查服务 → 解密 → 替换 {{keyname}} → HTTP 请求 → 透传
    - **注意**：Relay 不使用 `server.WriteJSON`，直接写 statusCode + Content-Type + body 到 ResponseWriter
  - `substitute(input, credentials)` — 占位符替换工具函数
  - 错误映射：404/502/504/400
- [ ] 验证：mock HTTP server，测试 relay 替换 + 透传

### 1.4 HTTP 处理层
- [ ] `backend/internal/handler/service.go`:
  - `Create` — 解析 JSON → 校验 → 调 Service.Create → 返回 201
  - `List` — 读取可选 `?server_id=` 查询参数 → 调 Service.List → 返回 200
  - `Relay` — 解析 relay body → 调 Service.Relay → 透传
- [ ] 验证：curl 测试 CRUD + relay

## Phase 2: Scope 中间件 + 路由

### 2.1 Scope 扩展
- [ ] `backend/internal/server/middleware/scope.go`:
  - `validScopes` 加 `"services:relay"`
  - `routeScopes` 加 `"POST /api/v1/services/{id}/relay": "services:relay"`
  - `jwtOnlyRoutes` 加 `"POST /api/v1/services": true`
- [ ] 验证：API key 无 scope → relay 403；加 scope → 200

### 2.2 路由注册
- [ ] `backend/internal/server/router.go`: 注册 `/api/v1/services` 路由组
- [ ] 验证：`GET /api/v1/services` → 200

### 2.3 依赖注入
- [ ] `backend/cmd/server/main.go`: NewServiceRepo → NewServiceRelayService → NewServiceHandler

## Phase 3: 前端

### 3.1 类型 + API 客户端
- [ ] `frontend/src/types/models.ts`: ServiceSchema (zod), ServiceFormSchema
- [ ] `frontend/src/features/services/api.ts`: createService, getServices

### 3.2 React Query Hooks
- [ ] `frontend/src/features/services/hooks/use-services.ts`: useServices, useCreateService

### 3.3 动态 Key-Value 组件
- [ ] `frontend/src/features/services/components/service-key-input.tsx`:
  - 状态：`{ key: string; value: string }[]`
  - [+ Add] / [- Remove] 按钮
  - key 重复校验、空值校验

### 3.4 服务表单 + 列表
- [ ] `frontend/src/features/services/components/service-form.tsx`: react-hook-form + zod, 含 ServiceKeyInput
- [ ] `frontend/src/features/services/components/service-list.tsx`: 表格展示 name/url/hints/server
- [ ] `frontend/src/features/services/components/service-list-page.tsx`: 组合 form + list

### 3.5 路由 + i18n
- [ ] `frontend/src/app/router.tsx`: `/services` → ServiceListPage
- [ ] `frontend/src/i18n/locales/en.json` + `zh-CN.json`: service 段

### 3.6 服务器详情集成
- [ ] `frontend/src/features/servers/components/server-detail.tsx`: 加 "Associated Services" 区域
- [ ] `GET /api/v1/services?server_id={id}` 查询

### 3.7 验证
- [ ] `npx tsc --noEmit` 无类型错误
- [ ] `npm run build` 构建成功

## Phase 4: 集成测试

- [ ] 启动完整 stack → 登录 Web UI
- [ ] 注册 Portainer 服务（多密钥）
- [ ] API key + services:relay scope → relay 成功
- [ ] 无 scope API key → relay 403
- [ ] JWT → 创建服务成功
- [ ] Relay 502/504 错误处理
- [ ] 前端服务列表正确展示
- [ ] 前端创建表单动态 key-value 正常工作
- [ ] `go build ./...` + `npm run build` 无错误

## 回滚点

- 每 Phase 完成后 git commit，出问题可逐阶段回滚
- Phase 1 结束后可独立验证（无 scope 限制，JWT 用户可调 relay）

## 总计

| 类别 | 新增文件 | 修改文件 | 行数估算 |
|------|---------|---------|---------|
| 后端 Model | 1 | 1 | ~30 |
| 后端 Repo | 1 | 0 | ~40 |
| 后端 Service | 1 | 0 | ~100 |
| 后端 Handler | 1 | 0 | ~80 |
| Scope + Router | 0 | 2 | ~10 |
| DI (main.go) | 0 | 1 | ~10 |
| 前端 Types | 0 | 1 | ~20 |
| 前端 API + Hooks | 2 | 0 | ~60 |
| 前端 Components | 4 | 0 | ~200 |
| 前端 Router + i18n | 0 | 3 | ~40 |
| **合计** | **10** | **8** | **~590** |
