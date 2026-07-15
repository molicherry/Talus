# 服务中继 — 通过Talus安全代理外部服务

## 目标

AI 需要通过 Talus 管理外部服务（Portainer、Cloudflare 等），但服务密钥不应暴露给 AI。Talus 作为中间代理：AI 调用 Talus API，Talus 解密密钥并转发请求，AI 全程不接触明文。

## 已确认信息

- 密钥使用和 SSH 凭证相同的 AES-256-GCM + master key 加密
- 服务可绑定服务器（`server_id` 可选 FK），也可独立存在
- 注册服务为 JWT-only 操作（仅人类通过 Web UI 操作）
- 中继需要 `services:relay` scope（API key 控制）
- 列表接口不返回密钥，仅返回 name、url、hint
- 不提供 DELETE（服务注册后长期存在）
- 复用现有 scope 中间件（`routeScopes` + `hasScope`）

## 数据模型

`services` 表：

| 字段 | 类型 | 说明 |
|------|------|------|
| id | uint | PK |
| server_id | *uint | 可选 FK → servers，`OnDelete:SET NULL` |
| name | string(128) | 服务标识，如 `portainer`，`gorm:"uniqueIndex"` |
| display_name | string(128) | 前台展示名 |
| base_url | string(512) | `http://172.18.0.4:9000` |
| encrypted_credentials | jsonb | `{"token":"<enc>"}`，AES-256-GCM + per-service random salt |
| credential_hints | jsonb | `{"token":"API token"}`，hints 的 key 必须与 encrypted_credentials 一致 |
| description | text | |
| salt | []byte | 随机 16 字节，和 SSH 凭证同模式 |
| created_at / updated_at | timestamp | |

## API

### POST /api/v1/services — 注册服务（JWT-only）

请求体：
```json
{
  "name": "portainer",
  "display_name": "Portainer CE",
  "base_url": "http://172.18.0.4:9000",
  "credentials": { "token": "ptr-abc123" },
  "credential_hints": { "token": "Portainer admin token" },
  "description": "容器管理面板",
  "server_id": null
}
```

校验：
- `name` 必填，1-128 字符，唯一
- `base_url` 必填，必须是 http/https
- `credentials` 必填，至少一个 key-value 对
- `credential_hints` 可选，key 必须与 `credentials` 一致
- `server_id` 可选，存在时必须是有效 server id

处理：生成随机 salt → 对 credentials 每个 value 分别 AES-256-GCM 加密 → 存入 `encrypted_credentials`

### GET /api/v1/services — 列出服务

返回所有服务。`encrypted_credentials` 不输出（`json:"-"`）。`salt` 不输出。

### POST /api/v1/services/{id}/relay — 中继请求（需 services:relay scope）

- Relay body：AI 发完整 HTTP 请求，密钥用 `{{keyname}}` 占位
  ```json
  {
    "method": "GET",
    "path": "/api/stacks",
    "headers": { "X-API-Key": "{{token}}" },
    "body": {}
  }
  ```
- Relay 行为：
  1. 用服务 id 查数据库，获得 `base_url`、`salt`、`encrypted_credentials`
  2. `DeriveKey(salt)` → 遍历解密所有 `encrypted_credentials` → 得到明文 key-value
  3. 用 `url.JoinPath(base_url, path)` 拼接完整 URL（正确处理首尾斜杠）
  4. 遍历 headers/path/body 中的 `{{keyname}}` 占位符，替换为解密后的值
  5. 发起 HTTP 请求到目标服务（timeout 30s）
  6. 透传响应：HTTP status code + response body + `Content-Type` header
     丢弃 hop-by-hop headers（`Transfer-Encoding`、`Connection`）
- 错误处理：目标不可达→502，超时→504，目标5xx原样透传，服务不存在→404
- Talus 不需要知道 auth_type，不区分 api_key/bearer/basic — 占位符替换完全由调用方控制

## Scope 扩展

- 新增 `services:relay` scope
- `routeScopes` 映射：`POST /api/v1/services/{id}/relay` → `services:relay`
- `validScopes` 白名单添加 `services:relay`
- `AllScopeGatedScopes` 默认值**不加**（新建 key 不应默认有 relay 权限）

## 安全边界

- 密钥在数据库加密存储，API 返回时 `json:"-"` 不输出
- Relay 接口：Talus 服务端解密 → 发起 HTTP → 返回结果，密钥不出进程
- 注册服务 JWT-only，AI 的 API key 无法注册/修改服务
- 支持多个密钥键值对（`encrypted_credentials` jsonb），每个值独立加密

## 待确认

- [x] ~~前端是否需要服务管理 UI？~~ → **需要前端 UI**
- [x] ~~API key 是否需要按服务粒度控制？~~ → **不需要，`services:relay` scope 可访问所有服务**
- [x] ~~是否需要 `allowed_methods` 白名单？~~ → **不需要，按 scope 粒度控制**
- [x] ~~auth_type 只需要 `api_key` 还是也需要 `bearer` / `basic`？~~ → **不需要 auth_type**。改为占位符模式：AI 发完整请求，密钥位用 `{{keyname}}` 占位，Talus 解密并替换后转发。支持多个密钥键值对。

## Acceptance Criteria

- [ ] 注册 Portainer 服务，存 `{"token":"<enc>"}`，带 salt 加密存储
- [ ] `GET /api/v1/services` 返回服务列表，`encrypted_credentials` 和 `salt` 不输出
- [ ] API key 直接调 relay → 403（无 scope），加 `services:relay` 后 → 成功
- [ ] JWT 用户调 `POST /api/v1/services` → 成功，API key 调用 → 403
- [ ] Relay 正确替换 headers、path、body 中的 `{{keyname}}` 占位符
- [ ] 多密钥：注册 `{"token":"enc","account_id":"enc"}` → relay 分别替换 `{{token}}` 和 `{{account_id}}`
- [ ] 目标不可达 → 502，超时 → 504，正常响应原样透传
- [ ] 前端可创建服务（含多凭据动态 key-value 输入）、查看服务列表、在服务器详情页看到关联服务

## 已知限制

- **无 UPDATE 端点**：密钥过期或 URL 变更需先重建服务（无 DELETE，旧记录保留为历史）
- **前端多凭据输入**：动态 key-value 表单在当前代码库中无先例，需新设计组件

## 不做

- 不做请求重放保护（v1）
- 不做频率限制
- 不做响应缓存
- 不做全流量反向代理
