# State Management

> How state is managed in this project.

---

## Overview

**No global state library.** This project uses:

| State Category | Solution | Where |
|---------------|----------|-------|
| Server state (API data) | TanStack Query v5 | `features/{domain}/hooks/` |
| Client/UI state | `useState` / `useReducer` | Inside the component that owns it |
| Auth state | JWT in localStorage + `useAuth()` hook | `hooks/use-auth.ts` |
| URL state | React Router `useSearchParams` | Component reading query params |
| Form state | shadcn/ui Form + react-hook-form | Form components |

---

## State Decision Tree

```
Is the data from the server?
├── YES → TanStack Query (with queryKey)
│         NEVER put in useState or useReducer
└── NO
    ├── Needed by multiple unrelated components?
    │   ├── YES → Lift to closest common ancestor (prop drilling or context)
    │   └── NO  → useState inside the component
    ├── Complex multi-field form with validation?
    │   └── react-hook-form + zod resolver
    ├── Reflects URL (filters, pagination)?
    │   └── useSearchParams (NOT useState)
    └── Persists across sessions (theme, settings)?
        └── localStorage + custom hook
```

---

## Server State (TanStack Query)

### QueryClient Configuration

```tsx
// app/query-client.ts
import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
      refetchOnWindowFocus: false, // for admin dashboards, not consumer apps
    },
  },
});
```

### Provider Wiring

```tsx
// app/providers.tsx
export function Providers({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        {children}
      </BrowserRouter>
    </QueryClientProvider>
  );
}
```

---

## Client State (useState / useReducer)

### When useState is enough

```tsx
// Modal open/close, toggle, simple input
const [isOpen, setIsOpen] = useState(false);
const [selectedIds, setSelectedIds] = useState<number[]>([]);
```

### When to use useReducer

Only when multiple state fields update together based on a single action:

```tsx
// Terminal session: connected + sessionId + history change atomically
type TerminalState = {
  status: "connecting" | "connected" | "disconnected";
  sessionId: string | null;
  output: string[];
};

type Action =
  | { type: "connected"; sessionId: string }
  | { type: "output"; data: string }
  | { type: "disconnected" };

function terminalReducer(state: TerminalState, action: Action): TerminalState {
  switch (action.type) {
    case "connected":
      return { ...state, status: "connected", sessionId: action.sessionId };
    case "output":
      return { ...state, output: [...state.output, action.data] };
    case "disconnected":
      return { status: "disconnected", sessionId: null, output: [] };
  }
}
```

---

## Auth State

JWT stored in `localStorage`. `useAuth()` hook provides current user + role:

```tsx
// hooks/use-auth.ts
import { jwtDecode } from "jwt-decode";

interface JwtPayload {
  sub: number;
  username: string;
  role: "admin" | "operator";
  exp: number;
}

function getToken(): string | null {
  return localStorage.getItem("auth_token");
}

export function useAuth() {
  const token = getToken();
  if (!token) return { user: null, isAuthenticated: false, isAdmin: false };

  const payload = jwtDecode<JwtPayload>(token);
  if (payload.exp * 1000 < Date.now()) {
    localStorage.removeItem("auth_token");
    return { user: null, isAuthenticated: false, isAdmin: false };
  }

  return {
    user: { id: payload.sub, username: payload.username, role: payload.role },
    isAuthenticated: true,
    isAdmin: payload.role === "admin",
  };
}
```

---

## URL State (useSearchParams)

For lists with filters, sort, and pagination:

```tsx
// features/audit/components/audit-list.tsx
const [searchParams, setSearchParams] = useSearchParams();
const page = Number(searchParams.get("page") || "1");
const action = searchParams.get("action") || "";

function setFilter(key: string, value: string) {
  setSearchParams(prev => {
    const next = new URLSearchParams(prev);
    if (value) next.set(key, value);
    else next.delete(key);
    next.set("page", "1"); // reset to page 1 on filter change
    return next;
  });
}
```

---

## Common Mistakes

- ❌ Putting server data in `useState` and calling `useEffect` to fetch — redundant with TanStack Query.
- ❌ `useState<Server[]>([])` for something `useServers()` already gives you.
- ❌ Prop drilling >3 levels — extract to context or refactor component tree.
- ❌ Zustand / Redux for this project — overkill. TanStack Query + URL state cover everything.
- ❌ Storing entire user object in global state — `useAuth()` derives it from token.
- ❌ `useEffect` with `fetch()` + `setState()` — this IS `useQuery`.
