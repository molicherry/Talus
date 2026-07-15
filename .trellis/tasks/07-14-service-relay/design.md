# 服务中继 — 技术设计

## 架构

```
AI/User → Talus API (chi router)
              ├─ POST /api/v1/services          [JWT-only, scope bypass]
              ├─ GET  /api/v1/services          [auth, no scope]
              └─ POST /api/v1/services/{id}/relay [services:relay scope]
                            │
                            ├─ 查 DB → base_url + salt + encrypted_credentials
                            ├─ DeriveKey(salt) → 逐 key 解密
                            ├─ url.JoinPath(base_url, path)
                            ├─ 替换 {{keyname}} → headers/path/body
                            └─ HTTP → target service → 透传响应
```

## 新增文件

| 文件 | 用途 |
|------|------|
| `backend/internal/model/service.go` | GORM 模型 |
| `backend/internal/repository/service.go` | 数据访问 |
| `backend/internal/service/service_relay.go` | 业务逻辑 + relay 实现 |
| `backend/internal/handler/service.go` | HTTP handler |
| `frontend/src/features/services/api.ts` | API 客户端 |
| `frontend/src/features/services/hooks/use-services.ts` | React Query hooks |
| `frontend/src/features/services/components/service-list.tsx` | 服务列表 |
| `frontend/src/features/services/components/service-form.tsx` | 注册表单（含动态 key-value） |
| `frontend/src/features/services/components/service-key-input.tsx` | 动态 key-value 子组件 |

## 修改文件

| 文件 | 改动 |
|------|------|
| `backend/internal/server/router.go` | 注册 3 个新路由 |
| `backend/internal/server/middleware/scope.go` | `validScopes` + `routeScopes` + `jwtOnlyRoutes` |
| `backend/internal/model/apikey.go` | 不加 scope（默认 key 无 relay 权限） |
| `backend/cmd/server/main.go` | DI 注入 ServiceService + ServiceHandler |
| `frontend/src/app/router.tsx` | `/services` 路由 |
| `frontend/src/features/servers/components/server-detail.tsx` | 内嵌关联服务卡片 |
| `frontend/src/i18n/locales/en.json` | 英文翻译 |
| `frontend/src/i18n/locales/zh-CN.json` | 中文翻译 |

## 数据模型

```go
// backend/internal/model/service.go
type Service struct {
    BaseModel
    ServerID             *uint             `gorm:"index;constraint:OnUpdate:CASCADE,OnDelete:SET NULL"`
    Name                 string            `gorm:"uniqueIndex;size:128;not null"`
    DisplayName          string            `gorm:"size:128"`
    BaseURL              string            `gorm:"size:512;not null"`
    EncryptedCredentials map[string]string `gorm:"type:jsonb;serializer:json" json:"-"`
    CredentialHints      map[string]string `gorm:"type:jsonb;serializer:json"`
    Description          *string           `gorm:"type:text"`
    Salt                 []byte            `gorm:"type:bytea;not null" json:"-"`
}
```

`BaseModel` 已提供 `ID`、`CreatedAt`、`UpdatedAt`、`DeletedAt`（GORM 软删除）。
`DeletedAt` 存在但不暴露 DELETE API — 仅作为 `uniqueIndex` 冲突的解决方案。

## Name 唯一性冲突处理

`name` 有 `uniqueIndex`，不提供 DELETE API。但用户可能注册错误需要"替换"：

**方案**：利用 GORM 软删除。admin 通过直接 DB 操作或未来 UPDATE 端点处理。当前阶段：如果唯一键冲突，API 返回 409 Conflict + 提示"name already exists"。用户换个名字即可。

## Relay 实现细节

### 占位符替换

在 `path`、headers 的 key/value、body（string）中逐 key 扫描 `{{keyname}}` 并替换。

```go
func substitute(input string, credentials map[string]string) string {
    for k, v := range credentials {
        input = strings.ReplaceAll(input, "{{"+k+"}}", v)
    }
    return input
}
```

- path 替换后不再做 URL encode（调用方负责）
- headers 的 key 和 value 都替换（允许 `{{keyname}}` 出现在 header name 中）
- body 按 string 替换（Content-Type 决定如何解释）

### HTTP 客户端

- 复用 `net/http` 标准库
- Timeout: 30s（PRD 指定）
- 不跟随重定向（返回 3xx 原样透传，由调用方决定）
- 禁用 HTTP/2 连接复用（每次请求新连接，避免状态泄漏）

### 错误映射

| 条件 | HTTP 状态 |
|------|----------|
| 服务 id 不存在 | 404 |
| 目标连接拒绝/DNS 失败 | 502 Bad Gateway |
| 目标超时（30s） | 504 Gateway Timeout |
| 目标返回任意 status | 原样透传 |
| relay body JSON 解析失败 | 400 |
| method 为空 | 400 |

### Hop-by-hop header 过滤

转发时丢弃以下 header（RFC 2616 §13.5.1）：
- `Connection`、`Keep-Alive`、`Proxy-Authenticate`、`Proxy-Authorization`
- `TE`、`Trailer`、`Transfer-Encoding`、`Upgrade`

### 响应透传

```
Talus Response:
  Status: <target status code>
  Header: Content-Type: <target content-type>
  Body: <target response body>
```

不转发其他 header，不设置 `X-Talus-Relay` 或类似标识头。

## Scope 中间件改动

```go
// middleware/scope.go — 新增

var validScopes = map[string]bool{
    // ... existing ...
    "services:relay": true,
}

var routeScopes = map[string]string{
    // ... existing ...
    "POST /api/v1/services/{id}/relay": "services:relay",
}

var jwtOnlyRoutes = map[string]bool{
    // ... existing ...
    "POST /api/v1/services": true,
}
```

`GET /api/v1/services` 不放任何限制 — JWT 和 API key 都可访问（API key 也需要能看到有哪些服务可 relay）。

## 路由注册

```go
// router.go
r.Route("/api/v1/services", func(r chi.Router) {
    r.Post("/", serviceHandler.Create)            // JWT-only
    r.Get("/", serviceHandler.List)               // public
    r.Post("/{id}/relay", serviceHandler.Relay)   // services:relay
})
```

## 前端设计

### 路由

`/services` → `ServiceListPage`（包含列表 + 创建表单）

### 组件树

```
ServiceListPage
├── ServiceForm (创建表单，模态或内联)
│   └── ServiceKeyInput (动态 key-value 行)
│       └── [+] 添加 / [-] 删除 按钮
└── ServiceList (表格)
    └── ServiceRow (name / url / hints / server)
```

### 动态 Key-Value 输入（ServiceKeyInput）

```
┌─────────────────────────────────────┐
│ Key: [token        ▼] Value: [••••] │ [-]  ← 删除此行
│ Key: [account_id   ▼] Value: [••••] │ [-]
│                          [+ Add Key] │  ← 添加新行
└─────────────────────────────────────┘
```

状态：`{ key: string; value: string }[]`，key 不允许重复且不为空。提交时转为 `credentials: { [key]: value }` 和 `credential_hints: { [key]: hint }`。

### 服务器详情集成

`server-detail.tsx` 中新增"Associated Services"区域。通过查询参数 `GET /api/v1/services?server_id=X` 获取（或嵌入 `GET /api/v1/servers/{id}` 响应）。

**选择**：用 `?server_id=` 查询参数。不在 server 响应中内嵌 — 减少耦合，按需加载。

### i18n 新增 key

```
en:
  service.title: "Services"
  service.name: "Name"
  service.displayName: "Display Name"
  service.baseUrl: "Base URL"
  service.credentials: "Credentials"
  service.addKey: "+ Add Key"
  service.removeKey: "Remove"
  service.hint: "Hint"
  service.description: "Description"
  service.server: "Server"
  service.noServer: "None (standalone)"
  service.create: "Create Service"
  service.toast.created: "Service created"
  service.toast.createFailed: "Failed to create service"

zh-CN:
  service.title: "服务"
  service.name: "标识"
  service.displayName: "显示名称"
  service.baseUrl: "基础 URL"
  service.credentials: "凭据"
  service.addKey: "+ 添加密钥"
  service.removeKey: "移除"
  service.hint: "提示"
  service.description: "描述"
  service.server: "关联服务器"
  service.noServer: "无（独立服务）"
  service.create: "创建服务"
  service.toast.created: "服务创建成功"
  service.toast.createFailed: "创建服务失败"
```

## 依赖注入

```go
// main.go
serviceRepo := repository.NewServiceRepo(db)
serviceSvc := service.NewServiceRelayService(serviceRepo, masterKey)
serviceHandler := handler.NewServiceHandler(serviceSvc)
```
