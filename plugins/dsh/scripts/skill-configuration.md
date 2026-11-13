In DeepSeek Harness, configure **Plugins → Talus → HTTPS URL / API key** and
save. The plugin owns the host credential broker and stores the key through the
DSH credentials service. If configuration is missing, ask the user to fill the
plugin form; never request the key in chat or read private credential storage.
Do not launch a second broker or configure a key environment variable.

Prefer the host tool `talus_request` for ordinary requests. Supply only business
inputs: a required relative Talus API `path`, and optional `method` (default
`GET`), `json`, `headers`, and `timeout_ms`. For example:

```json
{"method":"GET","path":"/api/v1/servers"}
```

The tool returns `{"status":...,"headers":{...},"body":"..."}`. Inspect
`status`, then parse `body` as JSON when appropriate; the usual Talus `data`
envelope is inside that parsed body. Request and response bodies are limited to
1 MiB, and responses must be text or JSON. The deadline defaults to 30000 ms;
`timeout_ms` accepts values up to 300000 ms and does not extend server budgets.

The host attaches Talus authentication to the configured HTTPS destination.
Never supply `X-API-Key` or retrieve it from configuration, environment, tool
results, or headers. Relay's nested JSON headers and credential placeholders
are business inputs; follow the selected service's `usage_guide`.

Use the bundled helper for **all binary uploads and downloads**, with
`--data-file` and `--output` respectively. Also use it for larger JSON/text
requests or responses, with `--json-file` or `--output`. The host tool does not
accept local file paths. Both routes use the same host broker credentials.
Each DSH shell call receives only
connection/helper paths as `DSH_TALUS_BROKER` and `DSH_TALUS_HELPER`.
Map them in **that same shell call** before running examples below:

```bash
export TALUS_BROKER="$DSH_TALUS_BROKER"
export TALUS_REQUEST="$DSH_TALUS_HELPER"
node "$TALUS_REQUEST" --broker "$TALUS_BROKER" \
  --method GET --path /api/v1/servers
```

For PowerShell, set `$env:TALUS_BROKER = $env:DSH_TALUS_BROKER` and
`$env:TALUS_REQUEST = $env:DSH_TALUS_HELPER` in that invocation, then run
`node $env:TALUS_REQUEST --broker $env:TALUS_BROKER ...`. These values contain
paths, not the API key. Do not search for credentials if the broker is unavailable;
ask the user to check the plugin settings. The generic Quick Start below applies
to this helper and its host broker.
