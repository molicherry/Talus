# Quality Guidelines

> Code quality standards for frontend development.

---

## Overview

- **Linter + Formatter**: Biome (replaces ESLint + Prettier)
- **Testing**: Vitest (unit) + Playwright (E2E)
- **Type checking**: `tsc --noEmit` in CI
- **Bundle analysis**: `vite-bundle-visualizer` (manual, before major releases)

---

## Biome Configuration

```json
// biome.json
{
  "$schema": "https://biomejs.dev/schemas/1.9.4/schema.json",
  "organizeImports": { "enabled": true },
  "linter": {
    "enabled": true,
    "rules": {
      "recommended": true,
      "correctness": {
        "noUnusedVariables": "error",
        "noUnusedImports": "error"
      },
      "suspicious": {
        "noArrayIndexKey": "error",
        "noExplicitAny": "error",
        "noConsole": "warn"
      },
      "style": {
        "useConst": "error",
        "useTemplate": "error"
      }
    }
  },
  "formatter": {
    "enabled": true,
    "indentStyle": "space",
    "indentWidth": 2,
    "lineWidth": 100
  },
  "javascript": {
    "formatter": {
      "quoteStyle": "double",
      "semicolons": "always",
      "trailingCommas": "all"
    }
  }
}
```

### Scripts

```json
{
  "scripts": {
    "lint": "biome check ./src",
    "lint:fix": "biome check --write ./src",
    "format": "biome format --write ./src",
    "typecheck": "tsc --noEmit",
    "test": "vitest run",
    "test:watch": "vitest",
    "test:e2e": "playwright test"
  }
}
```

---

## Forbidden Patterns

### Never

- ❌ `any` — use `unknown` + type guard, or Zod parse.
- ❌ `@ts-ignore` / `@ts-expect-error` — fix the root cause.
- ❌ `console.log` committed — use a proper logger or remove. Warn-level lint.
- ❌ `index` as React key — use stable IDs from data (`key={server.id}`).
- ❌ `useEffect` for data fetching — use TanStack Query.
- ❌ Direct DOM manipulation (`document.getElementById`, `querySelector`) — use React refs.
- ❌ `dangerouslySetInnerHTML` — sanitize with DOMPurify first if unavoidable.
- ❌ `setTimeout` for debouncing search inputs — use `useDebounce` hook.
- ❌ Inline API calls in components — put in `features/{domain}/api.ts`.

### Always

- ✅ Named exports — never `export default` for components.
- ✅ Zod schemas for all API response parsing.
- ✅ `aria-label` on icon-only buttons.
- ✅ `loading` state shown while TanStack Query `isLoading` is true.
- ✅ Error boundaries at feature level (wrap each page route).
- ✅ `useCallback` on callbacks passed as props to memoized child components.

---

## Testing Requirements

### Vitest (Unit)

Test hooks and utility functions:

```tsx
// features/servers/hooks/use-servers.test.ts
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useServers } from "./use-servers";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

test("returns servers on success", async () => {
  const { result } = renderHook(() => useServers(), { wrapper: createWrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toHaveLength(3);
});
```

### Playwright (E2E)

Critical user flows:

```tsx
// e2e/login.spec.ts
test("user can log in and see server list", async ({ page }) => {
  await page.goto("/login");
  await page.fill('input[name="username"]', "admin");
  await page.fill('input[name="password"]', "test123");
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL("/");
  await expect(page.locator("[data-testid='server-list']")).toBeVisible();
});
```

| Test Type | Scope | Command |
|-----------|-------|---------|
| Unit (Vitest) | Hooks, utils, components | `vitest run` |
| E2E (Playwright) | Critical paths only | `playwright test` |
| Type checking | Whole project | `tsc --noEmit` |

---

## Code Review Checklist

Before marking a PR ready:

- [ ] `biome check ./src` passes
- [ ] `tsc --noEmit` passes
- [ ] `vitest run` passes
- [ ] No `any`, `as`, `@ts-ignore` in diff
- [ ] All API responses parsed with Zod
- [ ] TanStack Query keys follow `[domain, ...ids]` convention
- [ ] Mutations invalidate relevant queries on success
- [ ] New components are named exports
- [ ] Icon-only buttons have `aria-label`
- [ ] Loading and error states handled (no white screen of death)
- [ ] No direct `fetch()` calls outside `api.ts`
- [ ] No `localStorage` direct access outside `useAuth()` hook

---

## Error Boundaries

Every page route is wrapped:

```tsx
// app/router.tsx
<Route path="/servers" element={
  <ErrorBoundary fallback={<ServerListError />}>
    <ServerList />
  </ErrorBoundary>
} />
```

Error boundary catches render errors, logs to console, shows a fallback UI — never a blank page.
