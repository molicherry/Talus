# Frontend tests

Zero-dependency test suites (plain `node` + the project's own `tsc`) that guard
the hand-rolled replacements introduced during the frontend bundle-optimization
session. Run with:

```bash
npm test          # from frontend/
# or
bash tests/run.sh
```

## Suites

| File | Tests | Why it exists |
|------|-------|---------------|
| `query-layer.test.mjs` | The **actual compiled** `src/lib/query.ts` cache layer (dedup, retry, error, array-prefix invalidation, inactive-stale marking) | Guards 3 real bugs: (1) `invalidateQueries` matched JSON-string prefixes instead of queryKey **arrays**, (2) inactive (unmounted) entries weren't marked stale, (3) concurrent fetches weren't deduped |
| `i18n-locale.test.mjs` | The **actual** `src/i18n/locales/*.json` (en↔zh-CN key parity, no plural forms, simple `{{name}}` placeholders, no empty strings) | Guards locale drift: a zh-only key silently falls back to English (or worse, renders the raw key) — caught the real `terminal.disconnected` missing-in-en bug |
| `query-flags.test.mjs` | The **actual** `computeQueryFlags` render-flag logic extracted from `useQuery` | Guards the isLoading first-render-flash bug: a fresh enabled query must report `isLoading=true` on first render (react-query parity), a disabled one `false` |

The `query-layer.test.mjs` suite compiles `src/lib/query.ts` to a gitignored
temp module first (the file is TypeScript with React imports, so it can't be
`import`ed directly by node). `tests/run.sh` handles that step.

> The `useQuery`/`useMutation` HOOK render flags (`isLoading`/`isPending` and
> the mutation reset semantics) are now covered via `computeQueryFlags`
> (query-flags.test.mjs) for the query side. The `useMutation` reset semantics
> still need a real React renderer to test the `setState` path — those were
> verified via live-browser E2E during the session (documented in
> `.auto/prompt.md`).
