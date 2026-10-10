# Tests

`tests/` is the central inventory for every test case. It does **not** replace
the language-native runners: Go keeps its in-package `*_test.go` (so white-box
tests can reach unexported identifiers), and the frontend keeps its
`frontend/tests/` Node/Playwright suites. This directory adds a single
machine-readable manifest, a deterministic human report, and one entry point.

## Single machine source

`tests/coverage.json` (schema `schema_version: 1`) is the only machine-readable
source. It defines:

- `runs[]` — executable unit of work: `run_id`, `suite`, `runner`, `cwd`,
  `command` (argv array), `timeout_seconds`, `environment`.
- `cases[]` — one entry per acceptance case (`TC-XX-YY`): requirement ids,
  `spec_ref`, English `scenario`, and one or more `implementations[]`.
- `gates{}` — pre-registered run sets per phase (`M1`–`M4`).
- `scan_exemptions[]` — exact paths exempt from the static content scan.

`tests/coverage.md` is **generated** from `coverage.json` and must never be
edited by hand:

```bash
node tests/render-coverage.mjs          # regenerate
node tests/render-coverage.mjs --check   # CI: fail on drift
```

## Running

```bash
bash tests/run.sh --suite fast            # logic + static assertions (no DB/remote)
bash tests/run.sh --suite integration     # isolated DB / TimescaleDB / image artifacts
bash tests/run.sh --suite ui              # production build + mock API (+ manual a11y)
bash tests/run.sh --suite e2e             # real frontend + Hub + controlled SSH peer
bash tests/run.sh --gate M1               # pre-registered phase gate
bash tests/run.sh --list                  # list runs and gates
```

Fail-closed rules enforced before any runner starts:

1. `coverage.json` must validate (schema, unique ids, references, suites,
   statuses, commands, exemptions).
2. Every **required** implementation in the selected set must be
   `implemented` or `manual`. A `planned` required item **blocks** the gate.
3. A selected run whose `environment.requires` is unmet fails explicitly; it is
   never silently skipped and never counted as passing.

`automation_status` describes the implementation, not a run: an implemented
case that was not executed is still `implemented`. "Not run / no evidence" is a
**run result** recorded in `tests/.artifacts/exec-<execution_id>.json`, never
written back into `automation_status`.

## Suites

| Suite | Needs | Notes |
| --- | --- | --- |
| `fast` | Go toolchain, Node | Adapter static check, coverage validation, Go unit tests, frontend Node tests |
| `integration` | `TEST_DATABASE_URL` | Migration/lock/interrupt/concurrency subset; TimescaleDB and image-artifact checks |
| `ui` | Playwright + Chromium | Production build against mock APIs; manual screen-reader records are listed separately |
| `e2e` | `TEST_HUB_URL`, `TEST_DATABASE_URL` | Real frontend + Hub; SSH via a controlled peer |

Mock-API browser checks are **UI regressions**, not real E2E. `e2e` is
automated; only the screen-reader records are manual.

## Static scan exemptions

`tests/static/adapters-removed.sh` reads `scan_exemptions` from
`coverage.json`. Exemptions are exact file paths (only `negative_fixture` may
declare a directory prefix); wildcards are rejected. The four planning
documents under `docs/` are registered because they describe the removal
itself, and the checker plus its negative fixtures are registered because they
must contain the banned patterns. Exemptions affect the content scan only;
they never bypass the "adapter directory must not exist" assertion.
