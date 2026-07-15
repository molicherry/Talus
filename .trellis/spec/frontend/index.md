# Frontend Development Guidelines

> Best practices for frontend development in this project.

---

## Overview

React 19 + TypeScript + Vite. shadcn/ui for UI primitives, Tremor for monitoring dashboards, TanStack Query for server state.

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | Feature-based layout: `features/{domain}/{components,hooks,api}` | ✅ |
| [Component Guidelines](./component-guidelines.md) | shadcn/ui patterns, cva variants, Tailwind, a11y | ✅ |
| [Hook Guidelines](./hook-guidelines.md) | TanStack Query patterns, query keys, WebSocket hooks | ✅ |
| [State Management](./state-management.md) | No global store — Query + URL state + useState decision tree | ✅ |
| [Quality Guidelines](./quality-guidelines.md) | Biome linting, Vitest + Playwright, code review checklist | ✅ |
| [Type Safety](./type-safety.md) | Zod schemas, API response validation, forbidden `any`/`as` | ✅ |

---

## Tech Stack Summary

| Component | Choice |
|-----------|--------|
| Framework | React 19 + TypeScript 5.7 |
| Build | Vite 6 |
| UI Library | shadcn/ui (Radix + Tailwind CSS v4) |
| Charts | Tremor (Recharts wrapper) |
| Server State | TanStack Query v5 |
| Forms | react-hook-form + Zod |
| Routing | React Router v7 |
| Lint + Format | Biome |
| Icons | lucide-react |
| Testing | Vitest (unit) + Playwright (E2E) |

---

**Language**: English.
