---
name: talus
description: >-
  Manage servers registered in a running Talus instance through its REST API:
  list/add/update servers, execute SSH commands, query monitoring metrics,
  transfer and edit files over SFTP, list credential metadata, and discover
  and call registered services through relay. Use when the user asks to operate
  Talus or its servers and services. Read each service's usage guide before
  calling it. API keys are scoped; administrator-only operations use the Web UI.
triggers: ["Talus", "manage server", "execute command on server", "check server metrics", "list credentials", "relay request", "list servers", "Talus 管理", "通过Talus执行命令", "proxied service", "代理服务"]
---

# Talus Skill — Agent Operation Guide

Talus is a self-hosted Linux server management platform. The Hub uses SSH/SFTP
for commands, terminals, files, and installation of a resident monitoring agent.
That agent runs under systemd and reports over outbound WSS, with HTTPS fallback;
monitoring does not require an additional inbound port on the node.

Keep this entire `talus/` directory when installing the skill. Detailed contracts
are bundled in [File API](references/files.md) and [Monitoring API](references/metrics.md);
they do not require a checkout of the application repository.

## Quick Start

Use the configured environment automatically. If `TALUS_API_KEY` is missing,
ask the user to configure it in the environment. Use `http://localhost:8080`
when `TALUS_URL` is unset; if that is not their instance, obtain the correct URL.
`TALUS_URL` is the instance base URL, without `/api/v1`.

```bash
TALUS_URL="${TALUS_URL:-http://localhost:8080}"
: "${TALUS_API_KEY:?Configure TALUS_API_KEY in the environment}"

curl --silent --show-error --fail-with-body \
  "${TALUS_URL%/}/api/v1/servers" \
  -H "X-API-Key: $TALUS_API_KEY"
```

Normal JSON responses use `{"data": ...}`. Talus errors use
`{"error":{"code":403,"reason":"forbidden","message":"..."}}`, with optional
`params`, field `details`, and `request_id`. Use HTTP status and `error.reason`
to diagnose errors; preserve `request_id` for troubleshooting. File/ZIP downloads
are binary, and Relay forwards the upstream status and body without a Talus envelope.
Do not print API keys or include them in URLs, generated reports, or debug traces.

## Authentication and permissions

Send `X-API-Key` on every management API request. Scopes and `server_ids` are
independent checks: omitted/empty `server_ids` allows all servers; `[1,3]` allows
only those servers. Services associated with a server also enforce that restriction.

| Action | Scope needed |
| --- | --- |
| List servers, summaries, or server detail | `servers:read` |
| Add/update servers (connection restrictions below) | `servers:write` |
| Execute command | `servers:exec` |
| Open WebSocket terminal | `servers:terminal` |
| Query metrics | `metrics:read` |
| List credentials (metadata only) | `credentials:read` |
| List/get services and their usage guides | `services:read` |
| Relay a service request | `services:relay` |
| Browse/stat/download files, read text, download ZIP | `files:read` |
| Upload/resume/status/discard own upload, mkdir, rename, save text | `files:write` |
| Delete files, links, or empty directories | `files:delete` |

**Defaults when `scopes` is omitted on key creation:** `servers:read`,
`servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`, `services:read`.
An explicitly empty scope list is rejected. `servers:write`, `services:relay`,
and all three file scopes are opt-in. Existing keys do not automatically gain
file permissions. Service discovery followed by Relay needs both service scopes.

### Operations requiring the Web UI

These routes reject API keys regardless of scopes. Paths are relative to `/api/v1`.

| Route | Operation |
| --- | --- |
| `DELETE /servers/{id}` | Delete server and uninstall its resident agent |
| `POST /servers/{id}/connection/test` | Test the SSH connection route |
| `POST /credentials`, `PUT /credentials/{id}`, `DELETE /credentials/{id}` | Mutate credentials |
| `GET /credentials/{id}/reveal` | Reveal a credential secret |
| `GET /api-keys`, `POST /api-keys`, `DELETE /api-keys/{id}` | Manage API keys |
| `GET /api-keys/{id}/reveal` | Reveal a stored API key |
| `POST /services`, `PUT /services/{id}`, `DELETE /services/{id}` | Mutate services |
| `GET /services/{id}/credentials` | Read service secrets |
| `GET /usage-logs`, `GET /usage-logs/filter-options`, `GET /usage-logs/{id}` | Read operation history |
| `GET /auth/profile`, `PUT /auth/password` | User account operations |

Administrator JWT checks also protect `POST /servers/{id}/host-key/trust` and
changes to the server's `connection` configuration (direct, SSH jump, or SOCKS5).
`servers:write` alone cannot perform them. API keys can use an administrator's
preconfigured route; omit `connection` when creating a server with the default
direct route or updating unrelated fields.

Changing `host`, `port`, or `credential_id` also requires an administrator when
the server has a proxy route or is used as another server's SSH jump host.

The public health/version/login/setup entry points do not authorize management
operations. `/agent/report` and `/agent/connect` use separate node authentication
and are not endpoints for this skill.

## Common tasks

### Find a server

```text
GET /api/v1/servers           → permitted servers, status, monitoring, latest_metrics
GET /api/v1/servers/summary   → lightweight list, connection/monitoring status; no metrics
GET /api/v1/servers/{id}      → detail, credential metadata, route and host-key state
GET /api/v1/credentials      → available credential metadata; no secret values
```

Use returned IDs instead of guessing. `status` is a compatibility summary
(`online`, `offline`, or `unknown`). Read `monitoring.connection`,
`monitoring.collection`, and `monitoring.data` separately before reporting health.
Missing metrics are not zero; a connected agent may have stale data or collection
errors. See the [monitoring status reference](references/metrics.md#monitoring-status).

### Execute a command

Resolve the target server first. For example, after selecting server ID 1:

```bash
curl --silent --show-error --fail-with-body \
  "${TALUS_URL%/}/api/v1/servers/1/exec" \
  -H "X-API-Key: $TALUS_API_KEY" -H 'Content-Type: application/json' \
  --data '{"command":"whoami","timeout":60}'
```

Response: `{"data":{"stdout":"deploy\n","stderr":"","exit_code":0}}`.
The account is the SSH credential's user, not necessarily root. Check `exit_code`
even after HTTP 200. The default timeout is 30 seconds and the maximum is 300;
the Hub can configure lower values. A timeout or lost response does not establish
whether a command changed remote state; inspect that state before retrying a mutation.

If `ssh_host_key_unverified` or `ssh_host_key_mismatch` is returned, an administrator
must verify the fingerprint independently and confirm it in the Web UI before
SSH commands, terminals, file operations, or agent installation can proceed.

For `GET /api/v1/servers/{id}/terminal`, a non-browser WebSocket client can send
`X-API-Key` during the upgrade. Do not put the key in the URL.

### Manage or transfer files

Use `/api/v1/servers/{id}/sftp` and read the bundled [File API](references/files.md)
for endpoint bodies, binary transfers, directory transfer, and recovery examples.

- Read text with `GET /text`, then send its `path`, edited `content`, and original
  `version` to `PUT /text`. On `409 sftp_conflict`, keep the draft and reconcile
  with a fresh read; do not silently overwrite it.
- Upload raw bytes, not base64 or JSON. After an interrupted resumable upload,
  query its status before retrying chunks or completing it.
- Resume downloads with both `Range` and the saved `ETag` in `If-Range`.
  A full `200` body replaces the earlier partial file; it must not be appended.
- File operations require the selected SSH account's permissions and an enabled
  SFTP subsystem. Do not replace failed file operations with shell commands or
  privilege elevation.

### Query metrics

```text
GET /api/v1/servers/{id}/metrics?interval=auto&stat=avg
```

By default this queries the last hour. `data` is an object containing `points`,
`requested_interval`, `actual_interval`, `stat`, `coverage`, `source`,
`coarsen_reason`, and `downsampled`, plus the server ID and time range.
Read `data.points`, not an array directly under `data`.

Use the [Monitoring API](references/metrics.md) for time parameters, average/peak
field selection, null values, query budgets, and handling 422/503 responses.

### Discover and use a registered service

1. List services with `GET /api/v1/services` (`services:read`). Use
   `?server_id=<id>` to narrow the directory to a server. Entries include
   `name`, `description`, and `usage_guide_excerpt`.
2. Read `GET /api/v1/services/{id}` for the full `usage_guide` and credential hints.
3. Construct the request according to that guide, then call
   `POST /api/v1/services/{id}/relay` (`services:relay`). If the guide is missing,
   obtain the target API contract before guessing endpoint or authentication details.

Example Relay body for a service whose guide specifies bearer authentication:

```json
{
  "method": "GET",
  "path": "/api/health",
  "headers": {"Authorization": "Bearer {{token}}"},
  "mode": "standard"
}
```

Send that JSON with `Content-Type: application/json` and `X-API-Key` to Talus.
The Hub calls the service's `base_url` directly; assigning a service to a server
does not create an SSH tunnel. Stored credentials replace `{{key}}` placeholders
in the target URL, header names/values, and body. Set authentication headers as
the guide specifies; Talus does not infer the target's authentication scheme.

- `path` is appended to the service base path, with dot segments normalized;
  its supplied non-empty query string is preserved. Follow the service's own parameter format and
  URL-encode values. Do not assume every service uses the same tRPC/query wrapper.
- For a target expecting a JSON object/array, pass that value directly as `body`;
  a pre-serialized JSON string is forwarded as a quoted string. Set the upstream
  `Content-Type` inside `headers` when required. Omit `body` for bodyless requests.
- Omitted/empty `mode` or `standard` has a 30-second total budget.
  `bounded_stream` explicitly allows up to 300 seconds. Upstream header wait
  and no-progress idle limits are 30 seconds; they do not extend the total budget.
  `mode` is not forwarded upstream.
- Upstream status, permitted response headers, and body are passed through,
  including upstream errors. Do not expect `data` around the result. Redirects
  are not followed; WebSocket/protocol upgrades are unsupported.
- A stream failure after headers aborts the response; it cannot append a Talus
  JSON error. Treat an interrupted body as incomplete. Check the upstream state
  before retrying a request that may have changed it.

Prefer Relay for registered services. A failed directory lookup is not evidence
that a service is unregistered: resolve missing scopes or connectivity first.
If the directory confirms it is absent, use direct server operations within
the user's requested scope. Never guess a registered service's URL or credentials.

## Server setup and updates

1. Have an administrator create the SSH credential in **Web UI → Credentials**.
   Find its ID with `GET /api/v1/credentials` if the key has `credentials:read`.
2. Register the server with `servers:write`:
   `POST /api/v1/servers` with
   `{"name":"prod-db","host":"10.0.1.10","port":22,"credential_id":1}`.
   Replace the example credential ID with the actual one. Configure a jump host
   or SOCKS5 route in the Web UI if needed.
3. Have an administrator test the connection, verify the presented host fingerprint,
   and confirm it in the Web UI. If the key is server-restricted, it must also
   permit the new server ID before subsequent calls.
4. Execute `whoami` with `servers:exec`; expect the configured SSH username and
   exit code 0. Do not assume root access.
5. For monitoring, the administrator must configure a node-reachable HTTPS
   `AGENT_HUB_URL` on the Hub. Installation needs SFTP, running systemd and `flock`;
   a non-root account needs user lingering. Talus does not automatically sudo.
   Check `monitoring.deployment` and data freshness after automatic deployment.

To update a server, read its current detail and send only intended fields via
`PUT /api/v1/servers/{id}`. Preserve omitted credentials; `credential_id:null`
explicitly clears the credential. Include the current `expected_connection_revision`
when an administrator changes `connection` through the Web UI/API; this revision
check applies to connection configuration mutations, not ordinary metadata updates.
Changing `host` or `port` clears the trusted host key, so the administrator must
verify the new fingerprint before SSH operations resume. If `connection_config_changed`
is returned, reload and reconcile before retrying.

Create services and API keys through the Web UI. Select the needed scopes from
the permission table and the intended server restrictions. API key values can
be revealed again in the Web UI when a recoverable value is stored; API keys
themselves cannot call reveal endpoints. Legacy keys without a stored value
cannot be revealed. Metadata/list endpoints do not return stored secret values.

## Troubleshooting

| Result | Next step |
| --- | --- |
| 401 | Check the configured key, instance URL, and whether the key was revoked; do not print the key. |
| 403 | Check Web UI/admin restrictions first, then the endpoint's scope, then `server_ids`. For Relay, distinguish a Talus refusal from an upstream 403. |
| `ssh_host_key_unverified` / `ssh_host_key_mismatch` | Have an administrator verify and confirm the fingerprint in the Web UI. |
| `connection_config_changed` | Reload server detail and reconcile the intended update with the current revision. |
| `sftp_conflict`, `sftp_exists`, or `sftp_not_empty` | Preserve the draft/source and inspect remote state; do not automatically overwrite or recursively delete. |
| 429 | Reduce concurrency and back off; serialize file operations for `sftp_busy`. |
| `metrics_query_budget_exceeded` (422) | Use the recommended interval or opt into coarsening; see the monitoring reference. |
| `metrics_rollup_not_ready` / `metrics_query_timeout` (503) | Report unavailable data; retry with backoff or a smaller range, without inventing zero samples. |
| Exec/Relay timeout or interrupted response | Check the operation's resulting state before replaying a mutation. |

If a key lacks a required scope or server ID, the administrator must create an
appropriately scoped replacement in the Web UI and configure it for the caller.
