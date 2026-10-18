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

Each `spec_ref` is a repository file path followed by an RFC 6901 JSON Pointer
to that case's English `scenario` in `coverage.json`; the inventory is
self-contained and does not require separate planning documents.

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
bash tests/run.sh --gate M3                               # monitoring, charts, agent deployment and image checks
bash tests/run.sh --gate M4                               # terminal UI and real frontend + Hub browser flow
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
   matching `run_id`, `status: passed`, a non-empty `evidence` list, an ancestor
   `commit`, and existing `covers` paths. Changes to covered code invalidate
   reuse; documentation-only changes are allowed. Missing or stale evidence
   stays `pending` and blocks.
5. A selected run whose `environment.requires` is unmet fails explicitly; it is
   never silently skipped and never counted as passing.
6. Go runs use native `go test -json -count=1` events. Every in-scope required
   implementation must emit a passing event from its declared package and test
   symbol. A missing test, a skipped test, or a skipped subscenario fails even
   when Go itself exits zero. Execution artifacts preserve these checks.
7. Node and browser runs must write fresh, run-scoped reports identifying each
   required implementation by source file and test name, with a passing result.
   A same-named test in another file cannot satisfy the check. Filtering required
   UI scenarios is rejected; zero executed cases do not count as passing.

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
| `integration` | `TEST_DATABASE_URL`; Docker for image checks | Migration/lock/interrupt/concurrency subset; TimescaleDB and image-artifact checks |
| `ui` | Playwright + Chromium | Production build against mock APIs; manual screen-reader records are listed separately |
| `e2e` | `TEST_DATABASE_URL`, Go, Node, curl; Playwright + Chromium for the browser flow | M2 starts real HTTP/WS handlers and a controlled SSH peer in Go; M4 builds and starts its own frontend, Hub, scratch database and SSH peer |

Mock-API browser checks are **UI regressions**, not real E2E. Deployment
verification is automated: M1's `deploy-verify` run starts the current Hub in an
isolated process, with its own temporary database and a local HTTP upstream. It
checks bootstrap/restart, session revocation, scoped service discovery, usage
guides, credential injection, and Relay modes. It needs no personal server,
public endpoint, historical commit or checked-in deployment evidence. The
optional screen-reader review still uses manual evidence.

M2 explicitly includes `go-race` and `terminal-e2e`. The latter drives the real
authentication and terminal handlers over HTTP/WebSocket against PostgreSQL and
a controlled SSH server; it is a protocol E2E test, not a mock browser test or a
substitute for M4's frontend flow. Each acceptance case may have several
required implementations, with an English `verification` explaining the behavior
each one proves. A constants-only test does not prove bounded cancellation.

Set `TEST_DATABASE_URL` to an isolated TimescaleDB instance for M1–M4 and the
complete Go suite. The full-process startup regression creates a uniquely named
database, runs the actual Hub entrypoint and migrations, verifies password
revocation across a restart, and drops only that database afterward. The test
database role therefore needs `CREATEDB` and permission to install the
TimescaleDB extension. The supplied database's tables are not migrated by this
startup test; other database tests use their own temporary schemas. CI uses the
same TimescaleDB PostgreSQL 16 image family as `docker-compose.yml` with a
test-only superuser. M1's migration prerequisite tests are separately indexed
as `TC-02-03`, so skipping their database setup cannot satisfy the gate.

CI runs M1–M4 separately and publishes fresh execution artifacts for the checked
out commit. M1 requires both isolated Hub acceptance tests to execute and pass;
a missing database or a skipped test blocks the gate. Squash/rebase merges need
no historical deployment record. These local checks do not access or authorize
deployment to a personal server. M4's browser flow also starts a local Hub and
does not require an external Hub URL.

## Release automation

```bash
node --test tests/release-policy.test.mjs
```

The separate **Release policy** CI job runs these checks for PRs, main and tags.
They use temporary Git repositories and simulated GitHub/registry responses to
exercise tag ancestry, immutability, stable/RC/test isolation, failed-CI and
evidence rejection, draft retries, numeric version ordering and out-of-order
promotion, recovery before the first enrolled release, edited titles and
idempotent label initialization. They never publish a real tag, Release or registry
image. The workflow also gates registry writes on the application suites and CodeQL. See the
[release runbook](../docs/releasing.md) for live-event verification and recovery.

## Static scan exemptions

`tests/static/adapters-removed.sh` reads `scan_exemptions` from
`coverage.json`. Exemptions are exact file paths (only `negative_fixture` may
declare a directory prefix); wildcards are rejected. Currently only the checker
itself is exempt because it must contain the banned patterns. Negative fixtures
are generated in temporary directories; they are not repository exemptions.
Any additional exemption must name an existing file and explain its purpose.
Exemptions affect the content scan only;
they never bypass the "adapter directory must not exist" assertion.
