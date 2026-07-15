# Type Safety

> Type safety patterns in this project.

---

## Overview

- **Language**: TypeScript 5.7+, strict mode
- **Validation**: Zod (runtime parsing of API responses, form inputs)
- **API types**: Shared between frontend and backend via manually synced interfaces (for now — later OpenAPI generation)

---

## Type Organization

```
src/types/
├── api.ts           # Response envelopes, API-level types
├── models.ts        # Domain models (User, Server, Credential, AuditLog)
├── ssh.ts           # SSH-specific types (exec response, terminal events)
└── metrics.ts       # Monitoring metric types
```

### API Response Envelope

```tsx
// types/api.ts
export interface ApiResponse<T> {
  data: T;
  meta?: PaginationMeta;
}

export interface ApiError {
  error: {
    code: number;
    message: string;
    details?: Array<{ field: string; message: string }>;
    request_id?: string;
  };
}

export interface PaginationMeta {
  total: number;
  page: number;
  per_page: number;
}
```

### Domain Models

```tsx
// types/models.ts
export interface User {
  id: number;
  username: string;
  role: "admin" | "operator";
  created_at: string;
}

export interface Server {
  id: number;
  name: string;
  host: string;
  port: number;
  description?: string;
  owner_id: number;
  status?: "online" | "offline" | "unknown";
  created_at: string;
}

export interface SSHCredential {
  id: number;
  server_id: number;
  auth_type: "password" | "private_key";
  username: string;
  key_fingerprint?: string; // only for private_key
  // encrypted_* fields NEVER reach the frontend
  created_at: string;
}
```

### SSH-Specific Types

```tsx
// types/ssh.ts
export interface ExecRequest {
  server_id: number;
  command: string;
  timeout?: number; // seconds, default 30
}

export interface ExecResponse {
  stdout: string;
  stderr: string;
  exit_code: number;
  duration_ms: number;
}

export interface TerminalEvent {
  type: "output" | "error" | "close";
  data?: string;
  exit_code?: number;
}
```

---

## Validation (Zod)

Every API response and form input must be validated at the boundary with Zod:

### API Response Validation

```tsx
// types/models.ts — schemas live alongside types
import { z } from "zod";

export const ServerSchema = z.object({
  id: z.number(),
  name: z.string(),
  host: z.string(),
  port: z.number().min(1).max(65535),
  description: z.string().optional(),
  owner_id: z.number(),
  status: z.enum(["online", "offline", "unknown"]).optional(),
  created_at: z.string(),
});

export type Server = z.infer<typeof ServerSchema>;

// features/servers/api.ts
export async function getServers(): Promise<Server[]> {
  const res = await apiClient.get("/servers");
  const json = await res.json();
  return z.array(ServerSchema).parse(json.data); // runtime validation
}
```

### Form Validation

```tsx
// features/servers/components/server-form.tsx
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";

const ServerFormSchema = z.object({
  name: z.string().min(1, "Name is required").max(128),
  host: z.string().min(1, "Host is required"),
  port: z.coerce.number().min(1).max(65535).default(22),
  description: z.string().max(500).optional(),
});

type ServerFormValues = z.infer<typeof ServerFormSchema>;

const form = useForm<ServerFormValues>({
  resolver: zodResolver(ServerFormSchema),
  defaultValues: { port: 22 },
});
```

---

## API Schema Evolution

When the backend API adds new fields that are not yet deployed or partially available, extend Zod schemas with `.optional()` to maintain backward compatibility:

```tsx
// ✅ Good: new fields as optional — existing API responses still parse
export const ServerSchema = z.object({
  id: z.number(),
  name: z.string(),
  // ...existing required fields...
  status: z.enum(["online", "offline", "checking", "unknown"]).optional(),
  last_seen: z.string().optional(),
  latest_metrics: z.object({
    cpu_percent: z.number().nullable().optional(),
    memory_percent: z.number().nullable().optional(),
    disk_percent: z.number().nullable().optional(),
  }).nullable().optional(),
});

// ❌ Bad: new fields as required — breaks when backend doesn't return them
export const ServerSchema = z.object({
  status: z.enum(["online", "offline"]),  // will throw ZodError if field missing
});
```

**Component usage**: Always use `??` fallbacks when consuming optional fields:

```tsx
const status = server.status ?? "unknown";
const cpuPercent = server.latest_metrics?.cpu_percent ?? null;
```

**Decision**: `.optional()` + `??` over conditional rendering. Reason: components stay simpler when they always have a value to render (even "N/A"), and the optional chain + nullish coalescing pattern is explicit about what happens when data is missing.

## Forbidden Patterns

- ❌ `any` — never. Use `unknown` if truly unknown, narrow with type guards or Zod.
- ❌ `as` type assertions on API data — use Zod `.parse()` instead.
- ❌ `@ts-ignore` or `@ts-expect-error` — fix the actual type error.
- ❌ `interface` in Zod schema files — derive types with `z.infer`, don't duplicate.
- ❌ Inline types for props (`props: { name: string }`) — always define an interface.
- ❌ Enums (`enum Status {}`) — use string unions (`type Status = "a" | "b"`), they're simpler in TypeScript strict mode.
- ❌ `!` non-null assertion — use optional chaining (`?.`) or explicit null check.

### Type Narrowing

```tsx
// ✅ Good: Type guard + exhaustive check
function getStatusLabel(status: Server["status"]): string {
  switch (status) {
    case "online":   return "Online";
    case "offline":  return "Offline";
    case "unknown":  return "Unknown";
    case undefined:  return "N/A";
  }
}

// ❌ Bad: as assertion
const server = data as Server;
```

---

## Common Patterns

### Discriminated Unions

```tsx
// For API responses that vary by type
type ExecResult =
  | { success: true; data: ExecResponse }
  | { success: false; error: string };

// Usage: TypeScript narrows after checking the discriminant
if (result.success) {
  console.log(result.data.stdout); // ✅ narrowed
} else {
  console.log(result.error);       // ✅ narrowed
}
```

### Generics for Reusable Components

```tsx
interface DataTableProps<T> {
  data: T[];
  columns: ColumnDef<T>[];
  onRowClick?: (row: T) => void;
}

// Usage: <DataTable<Server> data={servers} columns={serverColumns} />
```
