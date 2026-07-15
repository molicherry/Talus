# Database Guidelines

> Database patterns and conventions for this project.

---

## Overview

- **Database**: PostgreSQL 16+
- **Time-series extension**: TimescaleDB (for metrics hypertables)
- **ORM**: GORM v2
- **Migrations**: golang-migrate (CLI, file-based SQL)
- **Driver**: pgx v5 (via GORM postgres driver)

---

## GORM Model Conventions

Every model struct embeds a shared base:

```go
// internal/model/base.go
type BaseModel struct {
    ID        uint           `gorm:"primaryKey" json:"id"`
    CreatedAt time.Time      `json:"created_at"`
    UpdatedAt time.Time      `json:"updated_at"`
    DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}
```

Usage:

```go
type User struct {
    BaseModel
    Username string `gorm:"uniqueIndex;size:64;not null" json:"username"`
    Password string `gorm:"size:256;not null" json:"-"` // never serialized
}
```

### Rules

- Always embed `BaseModel` — never define ID/CreatedAt/UpdatedAt manually
- Use `json:"-"` on sensitive fields (password hash, encrypted keys)
- Use `gorm:"size:N"` to cap varchar columns
- All nullable columns must use pointer types or `sql.Null*`
- Indexes declared via tags: `gorm:"index"` or `gorm:"uniqueIndex"`

---

## Naming Conventions

| Target | Convention | Examples |
|--------|-----------|---------|
| Tables | `snake_case_plural` | `users`, `ssh_credentials`, `audit_logs` |
| Columns | `snake_case` | `server_id`, `created_by`, `key_fingerprint` |
| Foreign keys | `{table_singular}_id` | `user_id`, `credential_id` |
| Indexes | `idx_{table}_{column}` | `idx_users_username` |
| Unique constraints | `uq_{table}_{column}` | `uq_servers_name` |
| GORM struct | PascalCase, singular | `User`, `SSHCredential`, `AuditLog` |

---

## Core Tables

### users
```sql
CREATE TABLE users (
    id            SERIAL PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    role          VARCHAR(32)  NOT NULL DEFAULT 'operator',  -- admin | operator
    api_key_hash  VARCHAR(128) UNIQUE,                       -- for programmatic access
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);
```

### servers
```sql
CREATE TABLE servers (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(128) NOT NULL UNIQUE,
    host        VARCHAR(256) NOT NULL,
    port        INT          NOT NULL DEFAULT 22,
    description TEXT,
    owner_id    INT          NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);
```

### ssh_credentials
```sql
CREATE TABLE ssh_credentials (
    id                  SERIAL PRIMARY KEY,
    server_id           INT          NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    auth_type           VARCHAR(16)  NOT NULL,  -- 'password' | 'private_key'
    username            VARCHAR(64)  NOT NULL,
    encrypted_password  TEXT,                    -- AES-256-GCM encrypted, for auth_type=password
    encrypted_private_key TEXT,                  -- AES-256-GCM encrypted, for auth_type=private_key
    key_fingerprint     VARCHAR(128),            -- SHA256 fingerprint of public key
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);
```

### api_keys

```sql
CREATE TABLE api_keys (
    id          SERIAL PRIMARY KEY,
    user_id     INT          NOT NULL REFERENCES users(id),
    name        VARCHAR(128) NOT NULL,
    key_hash    VARCHAR(128) NOT NULL UNIQUE,
    key_prefix  VARCHAR(16)  NOT NULL,
    scopes      JSONB        NOT NULL DEFAULT '["servers:read","servers:write","servers:exec","servers:terminal","metrics:read","credentials:read"]',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
```

**scopes column**: JSONB array of `resource:action` strings controlling what the API key can access. Valid values: `servers:read`, `servers:write`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`. Migration must backfill existing rows with `UPDATE ... SET scopes = DEFAULT WHERE scopes IS NULL`. API keys never have access to JWT-only endpoints regardless of scope.

### audit_logs
```sql
CREATE TABLE audit_logs (
    id          SERIAL PRIMARY KEY,
    user_id     INT          NOT NULL REFERENCES users(id),
    server_id   INT          REFERENCES servers(id),
    action      VARCHAR(64)  NOT NULL,  -- 'exec.command', 'terminal.open', 'file.upload', ...
    detail      TEXT,                    -- JSON detail payload
    ip_address  INET,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_logs_user_id   ON audit_logs(user_id);
CREATE INDEX idx_audit_logs_action    ON audit_logs(action);
CREATE INDEX idx_audit_logs_created   ON audit_logs(created_at);
```

### metrics (TimescaleDB hypertable)
```sql
CREATE TABLE metrics (
    time       TIMESTAMPTZ NOT NULL,
    server_id   INT         NOT NULL,
    cpu_percent DOUBLE PRECISION,
    memory_used BIGINT,
    memory_total BIGINT,
    disk_used   BIGINT,
    disk_total  BIGINT,
    net_rx      BIGINT,
    net_tx      BIGINT,
    load_1      DOUBLE PRECISION,
    load_5      DOUBLE PRECISION,
    load_15     DOUBLE PRECISION
);

SELECT create_hypertable('metrics', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX idx_metrics_server_time ON metrics(server_id, time DESC);
```

---

## Migrations

golang-migrate manages raw SQL migration files:

```bash
# Create a new migration pair
migrate create -ext sql -dir migrations -seq create_users

# Apply all pending migrations
migrate -path migrations -database "$DATABASE_URL" up

# Rollback one migration
migrate -path migrations -database "$DATABASE_URL" down 1
```

### Migration Rules

- One migration per logical change. Don't bundle unrelated schema changes.
- Every `up` must have a working `down`. Test `up → down → up` before committing.
- No data migrations in schema migration files — separate script if needed.
- Always use `IF NOT EXISTS` / `IF EXISTS` for idempotent re-runs.
- Migration files named `NNNNNN_description.{up,down}.sql` with sequential numbers.

---

## Query Patterns

### Repository Layer — GORM

```go
// Correct: repository uses GORM methods, only returns data
func (r *serverRepo) FindByUser(ctx context.Context, userID uint) ([]model.Server, error) {
    var servers []model.Server
    err := r.db.WithContext(ctx).
        Where("owner_id = ?", userID).
        Order("name ASC").
        Find(&servers).Error
    return servers, err
}
```

### Transactions

Only in service layer, wrapping multiple repository calls:

```go
func (s *serverService) Transfer(ctx context.Context, serverID, newOwnerID uint) error {
    return s.db.Transaction(func(tx *gorm.DB) error {
        repo := repository.NewServerRepo(tx) // scoped to transaction
        server, err := repo.Find(ctx, serverID)
        if err != nil {
            return err
        }
        server.OwnerID = newOwnerID
        return repo.Update(ctx, server)
    })
}
```

### Pagination

Standard cursor-based offset pattern:

```go
func (r *auditRepo) List(ctx context.Context, userID uint, limit, offset int) ([]model.AuditLog, int64, error) {
    var logs []model.AuditLog
    var total int64
    base := r.db.WithContext(ctx).Where("user_id = ?", userID)
    base.Model(&model.AuditLog{}).Count(&total)
    err := base.Limit(limit).Offset(offset).Order("created_at DESC").Find(&logs).Error
    return logs, total, err
}
```

---

## Common Mistakes to Avoid

- ❌ Using `db.Exec()` with string concatenation → SQL injection. Always parameterize.
- ❌ Calling `db.Model()` inside a loop — N+1 query. Use `Preload()` or batch queries.
- ❌ Storing plaintext SSH keys in `encrypted_*` columns — always encrypt with the internal key before DB write.
- ❌ Running migrations in application code — migrations are a CLI step, not runtime.
- ❌ Using `SELECT *` — GORM's `Select()` should list columns explicitly when the table is wide.
- ❌ Sharing `*gorm.DB` across goroutines without `WithContext(ctx)` — each request gets its own context-scoped DB.

---

## Encryption at Rest

SSH credentials (`encrypted_password`, `encrypted_private_key`) are encrypted with AES-256-GCM before writing to the database. The master key is derived from `VPSMANAGER_MASTER_KEY` env var via Argon2id and never stored in the database. See `internal/pkg/crypto/` for implementation.

---

## JSONB Encrypted Credential Map + Per-Service Salt

Service credentials use the same AES-256-GCM scheme as SSH credentials, but adapted for a map of key-value pairs rather than a single field.

### Model

```go
// internal/model/service.go
type Service struct {
    BaseModel
    ServerID             *uint             `gorm:"index" json:"server_id,omitempty"`
    Name                 string            `gorm:"uniqueIndex;size:128;not null" json:"name"`
    BaseURL              string            `gorm:"size:512;not null" json:"base_url"`
    EncryptedCredentials map[string]string `gorm:"type:jsonb;serializer:json" json:"-"`
    CredentialHints      map[string]string `gorm:"type:jsonb;serializer:json" json:"credential_hints,omitempty"`
    Description          *string           `gorm:"type:text" json:"description,omitempty"`
    Salt                 []byte            `gorm:"type:bytea;not null" json:"-"`
}
```

### Key Design Decisions

- **Per-service random salt** (`Salt []byte` stored as `bytea`). Each service gets its own salt so that identical credential values across services produce different ciphertexts.
- **EncryptedCredentials is JSONB** via GORM's `serializer:json` tag. The map keys are the credential names; the values are hex-encoded AES-256-GCM ciphertexts.
- **Both fields use `json:"-"`** — the salt and encrypted credentials are never returned in API responses. The API returns `credential_hints` (placeholder descriptions) but never the actual values.
- **Salt is stored alongside the row**, not in a separate table. This keeps the dependency between encryption material and data explicit.

### Encryption Flow (Create)

```go
// internal/service/service_relay.go

func (s *ServiceRelayService) Create(ctx context.Context, input CreateServiceInput) (*model.Service, error) {
    // 1. Generate a random salt for this service.
    salt, _ := crypto.GenerateSalt()

    // 2. Derive an encryption key from the master key + salt.
    key := s.masterKey.DeriveKey(salt)

    // 3. Encrypt each credential value individually.
    encryptedCreds := make(map[string]string, len(input.Credentials))
    for k, v := range input.Credentials {
        encrypted, _ := crypto.Encrypt([]byte(v), key)
        encryptedCreds[k] = encrypted
    }

    // 4. Store salt + encrypted map.
    svc := &model.Service{
        EncryptedCredentials: encryptedCreds,
        Salt:                 salt,
        // ...
    }
    return svc, s.repo.Create(ctx, svc)
}
```

### Decryption Flow (Relay)

```go
// Decrypt all credentials using the per-service salt.
key := s.masterKey.DeriveKey(svc.Salt)
creds := make(map[string]string, len(svc.EncryptedCredentials))
for k, v := range svc.EncryptedCredentials {
    plain, _ := crypto.Decrypt(v, key)
    creds[k] = string(plain)
}
```

### Rules

- Always use `crypto.GenerateSalt()` — never hardcode or reuse salts.
- Encrypt each credential value individually, not the whole map. The caller needs to decrypt individual keys without loading the entire map into memory.
- Never log decrypted values or the derived key. The `crypto.MasterKey` type should implement `fmt.Stringer` to mask its value.
- Credential map keys are stored in plaintext (they are just names like "api_key", "token"). Only the values are encrypted.
- When a service is deleted, the salt and encrypted data are removed together (cascade via GORM soft-delete).

### Comparison with SSH Credential Encryption

| Aspect | SSH Credentials | Service Credentials |
|--------|----------------|---------------------|
| Data shape | Single string (`encrypted_password`, `encrypted_private_key`) | Map of strings (`encrypted_credentials`) |
| Salt | Per-credential | Per-service (shared across all pairs) |
| Encryption call | `crypto.Encrypt(value, key)` once | `crypto.Encrypt(value, key)` per key-value pair |
| DB column type | `TEXT` | `JSONB` (GORM `serializer:json`) |
| Decryption trigger | SSH connection attempt | Relay request |

---

## Encrypted Credentials — Common Mistakes

- ❌ Using a single global salt for all services — identical credentials produce identical ciphertexts, leaking information.
- ❌ Encrypting the entire `map[string]string` as one blob — prevents per-key selective decryption.
- ❌ Returning `encrypted_credentials` in API responses — always use `json:"-"` on sensitive fields.
- ❌ Storing the salt in a separate table — the salt must be atomically co-located with the encrypted data to avoid mismatches.
- ❌ Hardcoding salt generation with a fixed byte slice — always use `crypto/rand`.
