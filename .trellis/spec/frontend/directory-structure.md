# Directory Structure

> How frontend code is organized in this project.

---

## Overview

React 19 + TypeScript + Vite. shadcn/ui for UI primitives, Tremor for monitoring dashboards, Tailwind CSS v4 for styling.

```
frontend/
├── src/
│   ├── app/                     # App-level wiring
│   │   ├── router.tsx           # React Router route definitions
│   │   ├── query-client.ts      # TanStack Query configuration
│   │   └── providers.tsx        # QueryClientProvider, ThemeProvider, etc.
│   ├── features/                # Feature modules — one dir per domain
│   │   ├── auth/                # Login/logout, API key management
│   │   │   ├── components/      # LoginForm, ApiKeyCard, ...
│   │   │   ├── hooks/           # useLogin, useApiKeys, ...
│   │   │   └── api.ts           # API call functions (fetch wrappers)
│   │   ├── servers/             # Server CRUD
│   │   │   ├── components/      # ServerList, ServerForm, ServerDetail
│   │   │   ├── hooks/           # useServers, useCreateServer, ...
│   │   │   └── api.ts
│   │   ├── credentials/         # SSH credential management
│   │   │   ├── components/
│   │   │   ├── hooks/
│   │   │   └── api.ts
│   │   ├── terminal/            # WebSocket PTY terminal
│   │   │   ├── components/      # Terminal, TerminalSession
│   │   │   ├── hooks/           # useTerminal
│   │   │   └── socket.ts        # WebSocket connection management
│   │   ├── monitoring/          # Dashboard: charts, metrics
│   │   │   ├── components/      # CpuChart, MemoryGauge, DiskUsage
│   │   │   ├── hooks/           # useMetrics, useServerStatus
│   │   │   └── api.ts
│   │   ├── audit/               # Audit log viewer
│   │   │   ├── components/
│   │   │   ├── hooks/
│   │   │   └── api.ts
│   │   └── dashboard/           # Home page: overview, stats
│   │       ├── components/
│   │       └── hooks/
│   ├── components/              # Shared UI components
│   │   ├── ui/                  # shadcn/ui primitives (auto-generated)
│   │   │   ├── button.tsx
│   │   │   ├── dialog.tsx
│   │   │   ├── table.tsx
│   │   │   └── ...
│   │   ├── layout/              # App shell: Sidebar, Header, MainLayout
│   │   │   ├── sidebar.tsx
│   │   │   ├── header.tsx
│   │   │   └── main-layout.tsx
│   │   ├── status-badge.tsx     # Server status indicator
│   │   ├── confirm-dialog.tsx
│   │   └── empty-state.tsx
│   ├── lib/                     # Shared utilities
│   │   ├── api-client.ts        # fetch wrapper with auth header + error handling
│   │   ├── auth.ts              # token storage, refresh logic
│   │   ├── format.ts            # date, byte size, duration formatters
│   │   └── constants.ts
│   ├── hooks/                   # Shared (cross-feature) hooks
│   │   └── use-auth.ts          # Current user, permissions
│   ├── types/                   # Shared TypeScript types
│   │   ├── api.ts               # API response envelopes
│   │   ├── models.ts            # Domain model types (User, Server, etc.)
│   │   └── ssh.ts               # SSH-specific types
│   ├── main.tsx                 # ReactDOM.createRoot entry
│   └── index.css                # Tailwind imports + global styles
├── public/
├── index.html
├── vite.config.ts
├── tailwind.config.ts
├── tsconfig.json
├── biome.json                   # Biome lint + format config
└── package.json
```

---

## Feature Module Rules

Each feature directory is self-contained:

```
features/{domain}/
├── components/     # UI components specific to this domain
├── hooks/          # TanStack Query hooks + custom hooks — NEVER import hooks across features
├── api.ts          # Raw HTTP functions (fetch wrappers returning Promise<T>)
└── types.ts        # Domain-specific types (optional, if not in shared types/)
```

### Dependency Rules

```
feature/{X}/components ──► feature/{X}/hooks ──► feature/{X}/api.ts ──► lib/api-client.ts
       │                          │
       ├──► components/ui/*       ├──► hooks/use-auth.ts
       ├──► components/layout/*   ├──► types/*
       └──► lib/format.ts         └──► ❌ NEVER import hooks from another feature
```

---

## Naming Conventions

| Type | Convention | Example |
|------|-----------|---------|
| Component files | `kebab-case.tsx` | `server-form.tsx`, `cpu-chart.tsx` |
| Component names | `PascalCase` | `ServerForm`, `CpuChart` |
| Hook files | `use-{name}.ts` | `use-servers.ts`, `use-terminal.ts` |
| Hook names | `use{PascalCase}` | `useServers`, `useTerminal` |
| API files | `api.ts` (one per feature) | `features/servers/api.ts` |
| Type files | `kebab-case.ts` | `models.ts`, `api-response.ts` |
| Utility files | `kebab-case.ts` | `format.ts`, `api-client.ts` |

---

## Page → Feature Mapping

| Route | Feature Directory | Description |
|-------|-----------------|-------------|
| `/login` | `features/auth/` | Login form |
| `/` | `features/dashboard/` | Overview, recent activity |
| `/servers` | `features/servers/` | Server list + CRUD |
| `/servers/:id` | `features/servers/` | Server detail + actions |
| `/servers/:id/terminal` | `features/terminal/` | Interactive terminal |
| `/servers/:id/monitoring` | `features/monitoring/` | CPU, memory, disk charts |
| `/credentials` | `features/credentials/` | SSH credential management |
| `/audit` | `features/audit/` | Audit log viewer |
| `/settings/api-keys` | `features/auth/` | API key management |
