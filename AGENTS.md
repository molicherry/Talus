# Working on Talus

Talus is a single-user Go/React server manager with resident Linux and Windows monitoring agents. Start with the [development guide](docs/development.md) ([中文](docs/development.zh-CN.md)) and [architecture](docs/architecture.md) ([中文](docs/architecture.zh-CN.md)).

## Where changes belong

- `backend/cmd/server`: application wiring and startup; `backend/internal/server`: routes, middleware and errors.
- `backend/internal/handler`, `service`, `repository`, `model`: HTTP handling, operations, persistence and data models.
- `backend/cmd/agent`, `internal/agentclient`, `agentwire`, `agentwindows`: resident agent, reporting protocol and Windows lifecycle.
- `frontend/src/features`: feature APIs, hooks and views; `lib`: shared API/session/query logic; `i18n/locales`: English and Chinese copy.
- `tests/coverage.json`: acceptance inventory; `tests/coverage.md` is generated. Test and release procedures live in [tests/README.md](tests/README.md) and [docs/releasing.md](docs/releasing.md).

## Development rules

- Read the affected implementation before changing its documentation. Keep changes scoped to the requested behavior.
- Reuse backend authentication, scope checks, structured errors, SSH leases and usage recording. New routes need explicit authorization; API-key scope is separate from administrator JWT access.
- Preserve secret redaction, explicit SSH host-key trust, connection revisions and cancellation/cleanup ownership. Do not weaken these to make a fixture pass.
- Schema, encryption, API-key permissions and agent protocol changes need upgrade/restart coverage appropriate to their compatibility impact. Use isolated databases and controlled SSH peers.
- Use the shared frontend API client and query/session helpers. Keep English and Chinese locale keys and interpolation variables aligned.
- Update affected English/Chinese guide pairs in the same change. Keep current behavior in guides and historical rationale in clearly labeled design records. Link to the owning document instead of duplicating parameter or test inventories.
- The shipped `skills/talus/` is guidance for operating a running instance, not the development workflow. Keep its references self-contained when API behavior changes.

## Verification and handoff

Choose checks using the [development verification table](docs/development.md#verification). Run focused checks while iterating and the applicable acceptance gates before claiming that behavior is verified. Missing prerequisites or skipped tests are not passing evidence; report what ran and what did not.

For documentation changes, check relative links and run `git diff --check`, `node tests/render-coverage.mjs --check`, and `bash tests/static/adapters-removed.sh --repo`. Run `node frontend/tests/i18n-locale.test.mjs` when locale text changes. These checks do not establish semantic agreement with the implementation.

Do not edit generated coverage Markdown by hand. After changing its source inventory, run `node tests/render-coverage.mjs`. Keep secrets, generated binaries and local test output out of commits. Describe the changed behavior and validation in the handoff; pushing, publishing and release operations follow the user's requested scope.
