# Hook Guidelines

> How hooks are used in this project.

---

## Overview

- **Server state**: TanStack Query v5 (`@tanstack/react-query`)
- **Routing**: React Router v7 (`useParams`, `useNavigate`, `useSearchParams`)
- **Auth state**: Custom `useAuth()` hook (reads JWT from storage)
- **Terminal**: Custom `useTerminal()` hook (WebSocket management)

---

## TanStack Query Patterns

### Queries (Read)

```tsx
// features/servers/hooks/use-servers.ts
import { useQuery } from "@tanstack/react-query";
import { getServers } from "../api";

export function useServers() {
  return useQuery({
    queryKey: ["servers"],
    queryFn: getServers,
    staleTime: 30_000,       // 30s before refetch
    retry: 1,                 // one retry on failure
  });
}

// Component usage:
function ServerList() {
  const { data: servers, isLoading, error } = useServers();
  // ...
}
```

### Mutations (Write)

```tsx
// features/servers/hooks/use-create-server.ts
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createServer } from "../api";
import type { CreateServerPayload } from "../types";

export function useCreateServer() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (payload: CreateServerPayload) => createServer(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["servers"] });
    },
  });
}
```

### Query Key Convention

```
[domain, ...identifiers, ...filters]
```

| Pattern | Example |
|---------|---------|
| List all | `["servers"]` |
| Single by ID | `["servers", serverId]` |
| With filters | `["audit", { userId, action }]` |
| Metrics (time series) | `["metrics", serverId, timeRange]` |

---

## Custom Hook Patterns

### WebSocket Hook (Terminal)

```tsx
// features/terminal/hooks/use-terminal.ts
export function useTerminal(serverId: number) {
  const [status, setStatus] = useState<"connecting" | "connected" | "disconnected">("disconnected");
  const wsRef = useRef<WebSocket | null>(null);

  const connect = useCallback(() => {
    const token = getAuthToken();
    const ws = new WebSocket(`wss://hub/terminal/${serverId}?token=${token}`);
    // ... event handlers
  }, [serverId]);

  const send = useCallback((data: string) => {
    wsRef.current?.send(JSON.stringify({ type: "input", data }));
  }, []);

  useEffect(() => {
    return () => wsRef.current?.close(); // cleanup on unmount
  }, []);

  return { status, connect, send };
}
```

### Auth Hook (Global)

```tsx
// hooks/use-auth.ts
export function useAuth() {
  const token = getAuthToken();
  const user = token ? parseJWT(token) : null;

  return {
    user,
    isAuthenticated: !!user,
    isAdmin: user?.role === "admin",
  };
}
```

---

## Naming Conventions

| Type | Pattern | Examples |
|------|---------|---------|
| Query hooks | `use{Domain}` (list) / `use{Domain}Item` (single) | `useServers`, `useServer` |
| Mutation hooks | `use{Action}{Domain}` | `useCreateServer`, `useDeleteCredential` |
| Utility hooks | `use{Behavior}` | `useDebounce`, `useMediaQuery` |
| WebSocket hooks | `use{Connection}` | `useTerminal` |

---

## Rules

- Every feature's data fetching goes through a hook in `features/{domain}/hooks/`. Components never call `api.ts` directly.
- Mutation hooks always call `queryClient.invalidateQueries()` on success — never manually update cache.
- `useQuery` `staleTime` defaults:
  - CRUD lists: `30_000` (30s)
  - Single resource: `60_000` (1m)
  - Metrics (real-time): `5_000` (5s)
  - Audit logs: `60_000`
- Custom hooks must return a stable reference (memoize with `useMemo`/`useCallback` where needed).
- Always clean up in `useEffect` return (close WebSocket, clear intervals).

---

## Common Mistakes

- ❌ Calling API directly in components: `useEffect(() => { fetch(...) }, [])` — use TanStack Query.
- ❌ `useQuery` without `queryKey` — TanStack Query keys are the cache identity.
- ❌ `useMutation` without `onSuccess` invalidation — stale cache after writes.
- ❌ Updating cache manually (`queryClient.setQueryData`) instead of `invalidateQueries` — manual cache is the source of 90% of stale-data bugs.
- ❌ Not closing WebSocket on unmount — leaks connections.
- ❌ `useEffect` with a dependency that's a new object every render — use `useMemo` or primitive values.
