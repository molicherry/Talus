# Test coverage

> Generated from `tests/coverage.json` by `tests/render-coverage.mjs`. Do not edit by hand.

Cases: 32 · Runs: 8 · Schema: 1

## Runs

| run_id | suite | runner | cwd | command | timeout |
| --- | --- | --- | --- | --- | ---: |
| coverage-tools | fast | node-test | `.` | `node tests/render-coverage.mjs --check` | 120s |
| static-adapters | fast | shell | `.` | `bash tests/static/adapters-removed.sh` | 120s |
| go-unit | fast | go-test | `backend` | `go test ./...` | 600s |
| frontend-node | fast | node-test | `frontend` | `bash tests/run.sh` | 600s |
| migrate-hardening | integration | go-test | `backend` | `go test ./internal/repository/... -run ApplyOnce -count=1` | 600s |
| integration-agent | integration | go-test | `backend` | `go test ./internal/service/... -run Agent -count=1` | 600s |
| ui-charts | ui | playwright | `frontend` | `npm run test:ui` | 900s |
| e2e-core | e2e | shell | `.` | `bash tests/e2e/run.sh` | 1200s |

## Gates (pre-registered required runs)

- **M1**: `coverage-tools`, `static-adapters`, `go-unit`, `frontend-node`, `migrate-hardening`
- **M2**: `coverage-tools`, `static-adapters`, `go-unit`, `frontend-node`, `migrate-hardening`, `integration-agent`
- **M3**: `go-unit`, `frontend-node`, `migrate-hardening`, `integration-agent`, `ui-charts`
- **M4**: `go-unit`, `frontend-node`, `ui-charts`, `e2e-core`

## Phases (requirement scope per gate)

- **M1**: REQ-01, REQ-02
- **M2**: REQ-05, REQ-06, REQ-07
- **M3**: REQ-04, REQ-08
- **M4**: REQ-09

## Cases

| case_id | requirement | status | run | implementation | required | scenario |
| --- | --- | --- | --- | --- | --- | --- |
| TC-01-01 | REQ-01 | implemented | static-adapters | `tests/static/adapters-removed.sh:main` | yes | Remove adapters while retaining the skill and valid installation links. |
| TC-01-02 | REQ-01 | planned | e2e-core | — | yes | Preserve the service catalog and Relay contracts required by the skill. |
| TC-01-03 | REQ-01 | implemented | static-adapters | `tests/static/adapters-removed.sh:negative-fixtures` | yes | Reject residual adapter artifacts, stale installation links, missing skills, and invalid scan conditions; allow reviewed historical records. |
| TC-02-01 | REQ-02 | implemented | coverage-tools | `tests/render-coverage.mjs:--check` | yes | Index existing tests with English descriptions, generate coverage.md deterministically from coverage.json, reject report drift, and propagate runner failures. |
| TC-02-02 | REQ-02 | implemented | coverage-tools | `tests/run-suites.mjs:validate` | yes | Validate JSON schema, unique IDs, references, suites, statuses, and commands before execution; fail on missing dependencies and distinguish mock UI from real E2E. |
| TC-04-01 | REQ-04 | planned | ui-charts | — | yes | Apply correct time ranges, aggregation intervals, axes, and units; distinguish zero from null without implying finer sampling. |
| TC-04-02 | REQ-04 | planned | migrate-hardening | — | yes | Compute averages and observed sample maxima correctly; retain maxima in long-range data. |
| TC-04-03 | REQ-04 | planned | ui-charts | — | yes | Ignore stale responses during rapid range changes; support touch, keyboard input, and visibility-aware refresh. |
| TC-04-04 | REQ-04 | planned | ui-charts | — | yes | Distinguish series without color, meet contrast thresholds, preserve visible focus, and expose equivalent chart data to keyboard and screen-reader users. |
| TC-04-05 | REQ-04 | planned | integration-agent | — | yes | Enforce 600-bucket and 12,000-raw-sample boundaries; share one 2-second deadline across probes and plans; verify strict/coarsen behavior and 422/503 errors without range truncation. |
| TC-05-01 | REQ-05 | planned | integration-agent | — | yes | Commit or roll back password and token-version changes atomically; reject old JWTs and accept newly issued JWTs. |
| TC-05-02 | REQ-05 | planned | e2e-core | — | yes | Reject revoked JWTs during WS authentication and close existing sessions, including registration races. |
| TC-05-03 | REQ-05 | planned | integration-agent | — | yes | Clear authentication across tabs, keep API Keys independent, and reject legacy JWTs without a version claim. |
| TC-05-04 | REQ-05 | planned | integration-agent | — | yes | Serialize registration, ready admission, and revocation around COMMIT; cancel preparing sessions and reject new ready or input permissions for revoked versions. |
| TC-05-05 | REQ-05 | planned | integration-agent | — | yes | Read the primary database without positive version caching, enforce authentication budgets, and reject admission during unresolved commits, including restart recovery. |
| TC-06-01 | REQ-06 | planned | integration-agent | — | yes | Release upload leases on success and every error path; preserve capacity after repeated uploads. |
| TC-06-02 | REQ-06 | planned | integration-agent | — | yes | Recover from blocked or canceled uploads and prevent stale-generation lease returns. |
| TC-06-03 | REQ-06 | planned | integration-agent | — | yes | Reuse one target-pinned lease, propagate copy errors, and reclaim resources within one absolute 3-second deadline with a 100-ms grace; nested deployment and Monitor stages never reset it. |
| TC-07-01 | REQ-07 | planned | e2e-core | — | yes | Return complete results for 20-30-second commands and honor the configured default timeout. |
| TC-07-02 | REQ-07 | planned | go-unit | — | yes | Enforce an 8-MiB retained-output total and 4-MiB per-stream limits, distinguish exact-limit from discarded output, preserve true exit status, and reclaim resources after timeout or cancellation. |
| TC-07-03 | REQ-07 | planned | integration-agent | — | yes | Enforce explicit ordinary/stream Relay modes with 30/300-second totals and a 30-second progress idle limit; handle header/body timeouts, write failures, and cancellation. |
| TC-07-04 | REQ-07 | planned | integration-agent | — | yes | Verify 100-ms SSH/Relay and 2-second terminal graces inside one absolute 3-second teardown deadline; preserve the original outcome and never reset budgets or falsely publish done/release. |
| TC-08-01 | REQ-08 | planned | go-unit | — | yes | Enforce min(nodes, 16) active collections, one in-flight task per node, bounded pending work, capped jittered backoff, cancellation exclusions, successful recovery, and shutdown cleanup. |
| TC-08-02 | REQ-08 | planned | integration-agent | — | yes | Select architecture-compatible Agents, check version/hash, update atomically, report permission failures, and reconcile unknown activation outcomes. |
| TC-08-03 | REQ-08 | planned | migrate-hardening | — | yes | Calculate adjacent-sample rates correctly across resets, gaps, and duplicate timestamps; retain observed maxima. |
| TC-08-04 | REQ-08 | planned | integration-agent | — | yes | Probe the target OS and architecture before upload; preserve failure classes and perform zero uploads when probing or artifact selection fails. |
| TC-08-05 | REQ-08 | planned | integration-agent | — | yes | Verify both Agent architectures, manifest hashes, and protocol output inside every amd64 and arm64 Hub image built from either Dockerfile. |
| TC-08-06 | REQ-08 | planned | migrate-hardening | — | yes | Enforce 35-day retention and six-hour rollups, trim rolling-window partial buckets, recover aggregation gaps beyond 48 hours, preserve statistics, and measure storage costs. |
| TC-08-07 | REQ-08 | planned | integration-agent | — | yes | Correlate scheduled collections and deployment attempts, count failures and skips, and emit alerts at the defined lag and retention thresholds. |
| TC-08-08 | REQ-08 | planned | integration-agent | — | yes | Fail startup before HTTP/Monitor on invalid bundled artifacts; accept only matching protocol/version JSON, reject legacy output without metric insertion, and preserve node isolation and history. |
| TC-09-01 | REQ-09 | planned | ui-charts | — | yes | Provide usable toolbar controls, search, font sizing, fullscreen, clipboard actions, and client-side clearing. |
| TC-09-02 | REQ-09 | planned | e2e-core | — | yes | Support 375px and landscape layouts, PTY resizing, stable focus, and reconnect cleanup. |

## Scan exemptions

| path | kind | reason |
| --- | --- | --- |
| `docs/ARCHITECTURE.zh-CN.md` | planning | Planning document that references the removed adapter directory by design. |
| `docs/REQUIREMENTS.zh-CN.md` | planning | Planning document that specifies the adapter removal requirement. |
| `docs/IMPLEMENTATION.zh-CN.md` | planning | Planning document that describes the adapter removal work package. |
| `docs/DEVELOPMENT-PLAN.zh-CN.md` | planning | Planning document that describes the adapter removal task. |
| `tests/static/adapters-removed.sh` | checker | The checker itself must contain the banned patterns it searches for. |
| `tests/fixtures/adapters-removal/` | negative_fixture | Negative fixtures intentionally contain banned adapter artifacts. |
