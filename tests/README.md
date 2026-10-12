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
bash tests/run.sh --gate M1                               # pre-registered phase gate (recommended)
bash tests/run.sh --gate M2                               # isolated DB, race, real HTTP/WS/SSH terminal chain
bash tests/run.sh --suite fast --phase M1                 # classification, scoped to a phase
bash tests/run.sh --suite integration --phase M2
bash tests/run.sh --suite ui --phase M3
bash tests/run.sh --suite e2e --phase M4
bash tests/run.sh --list                                  # list runs and gates
```

`--suite` without `--phase` is strict: it blocks on every required item mapped to
a selected run, across all phases. Pass `--phase` to scope the blocking rule to
one phase's requirements. Use `--gate` for normal phase verification.

Fail-closed rules enforced before any runner starts:

1. `coverage.json` must validate: schema, unique ids, real requirement ids,
   referenced runs, existing implementation paths and Go test symbols, statuses, commands, and
   exemptions (a non-fixture exemption must be an existing file, not a
   directory).
2. A **gate** must cover every in-scope required case with one of its runs;
   dropping a run that covers a required case fails the gate.
3. Every **required** implementation in scope must be `implemented` or `manual`.
   A `planned` required item **blocks** gate and suite alike.
4. A required **manual** run must have `tests/evidence/<run_id>.json` with
   `status: passed` and a non-empty `evidence` list; otherwise it stays
   `pending` and blocks.
5. A selected run whose `environment.requires` is unmet fails explicitly; it is
   never silently skipped and never counted as passing.
6. Go runs use native `go test -json -count=1` events. Every in-scope required
   implementation must emit a passing event from its declared package and test
   symbol. A missing test, a skipped test, or a skipped subscenario fails even
   when Go itself exits zero. Execution artifacts preserve these checks.

Each invocation writes `tests/.artifacts/exec-<execution_id>.json` (selector,
commit, timestamps, per-run status, environment). That file is a **run result**
and is never written back into `automation_status`.

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
| `e2e` | `TEST_DATABASE_URL`; `TEST_HUB_URL` for the browser flow | M2 starts real HTTP/WS handlers and a controlled SSH peer in Go; M4 adds the real frontend + Hub flow |

Mock-API browser checks are **UI regressions**, not real E2E. `e2e` is
automated; only the screen-reader records are manual.

M2 explicitly includes `go-race` and `terminal-e2e`. The latter drives the real
authentication and terminal handlers over HTTP/WebSocket against PostgreSQL and
a controlled SSH server; it is a protocol E2E test, not a mock browser test or a
substitute for M4's frontend flow. Each acceptance case may have several
required implementations, with an English `verification` explaining the behavior
each one proves. A constants-only test does not prove bounded cancellation.

Set `TEST_DATABASE_URL` to an isolated TimescaleDB instance for M1/M2 and the
complete Go suite. The full-process startup regression creates a uniquely named
database, runs the actual Hub entrypoint and migrations, verifies password
revocation across a restart, and drops only that database afterward. The test
database role therefore needs `CREATEDB` and permission to install the
TimescaleDB extension. The supplied database's tables are not migrated by this
startup test; other database tests use their own temporary schemas. CI uses the
same TimescaleDB PostgreSQL 16 image family as `docker-compose.yml` with a
test-only superuser. M1's migration prerequisite tests are separately indexed
as `TC-02-03`, so skipping their database setup cannot satisfy the gate.

CI runs M1 and M2 independently and publishes their execution artifacts. Changed
deployment-covered code invalidates the old manual M1 evidence; M2 success does
not clear that pending result or authorize a deployment.

## Static scan exemptions

`tests/static/adapters-removed.sh` reads `scan_exemptions` from
`coverage.json`. Exemptions are exact file paths (only `negative_fixture` may
declare a directory prefix); wildcards are rejected. The four planning
documents under `docs/` are registered because they describe the removal
itself, and the checker plus its negative fixtures are registered because they
must contain the banned patterns. Exemptions affect the content scan only;
they never bypass the "adapter directory must not exist" assertion.
