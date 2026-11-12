In DeepSeek Harness, configure **Plugins → Talus → Talus URL / API key**, then
save. If a key is missing, ask the user to fill it in that plugin page; never ask
them to paste it into chat. Saved UI values take priority over the launching
environment's `TALUS_URL` and `TALUS_API_KEY`.

Each DSH shell-tool call receives the current settings as `DSH_TALUS_URL` and
`DSH_TALUS_API_KEY`. Before running **any** example below or in the references,
initialize the ordinary Talus variables **in the same shell call**:

```bash
export TALUS_URL="$DSH_TALUS_URL"
export TALUS_API_KEY="$DSH_TALUS_API_KEY"
```

For PowerShell, use `$env:TALUS_URL = $env:DSH_TALUS_URL` and
`$env:TALUS_API_KEY = $env:DSH_TALUS_API_KEY` in that invocation. Never echo these
values or enable shell tracing. These variables are already injected by the
plugin; do not read the DSH settings file to retrieve credentials. The generic
Quick Start below applies after this initialization.
