# Talus for DeepSeek Harness

English | [简体中文](README.zh-CN.md)

`dsh-plugin-talus` 0.1.0 bundles the [Talus skill](skills/talus/SKILL.md) for
DeepSeek Harness (dsh), with a host request tool and credential broker. It supports
server management, SSH execution, monitoring, SFTP, and service relay. The complete
skill, helpers, and references are included; installation does not require the
rest of the Talus source repository.

## Requirements

- Node.js 24.9.0 or later and DSH 0.2.2-rc.1. The package's `engines.dsh` range is
  `~0.2.2-rc.1`; dsh's plugin API is still in preview.
- A running Talus instance reachable from the dsh host over HTTPS, with a trusted
  TLS certificate. HTTP URLs and certificate-validation bypasses are not supported.
- A Talus API key with scopes for the intended tasks. See the bundled
  [permission guide](skills/talus/SKILL.md#authentication-and-permissions).

## Install

From the Talus repository root, install into the Web profile:

```bash
npm ci --ignore-scripts --prefix ./plugins/dsh
dsh plugin --profile web add ./plugins/dsh
```

For an independent copy of this plugin directory, use its absolute path instead
of `./plugins/dsh`. A directory installation links the directory and does not
install its dependencies: run `npm ci --ignore-scripts` in a source checkout
first, or `npm install --omit=dev --ignore-scripts` in an extracted package
directory without a lockfile. The third-party plugin interface also accepts a
local absolute directory. Restart the selected profile after installation.

Some DSH SDK packages declare additional host services as peers, so pnpm may
still report indirect peer warnings. Verify the Talus component is running in
the selected profile; warnings alone do not confirm that it loaded. Keep the
bundled entry id `talus` unchanged if editing the profile patch: the settings
panel uses that id.

Use `--profile headless` for a headless profile. For Desktop, launch Desktop once
to initialize its profile, fully quit it, then install with `--profile desktop`
and relaunch. Installing into `web` does not install into the other profiles.

## Configure in the plugin page

Open dsh over HTTPS or at `localhost` / a loopback address. An HTTP page on a
non-loopback host is not a secure browser context and cannot save credentials;
use HTTPS for remote access. Then open **Plugins → Talus**. Enter the HTTPS instance base URL, such as
`https://talus.example.com`, without `/api/v1`, and the API key from
**Talus Web UI → API Keys**, then save. Changes apply to later requests without
reinstalling the plugin or restarting dsh. The plugin starts its host broker
itself; do not manually launch the standalone broker.

The password field never displays the saved key. Leave it blank to keep the
credential for the same URL, or choose **Clear saved API key** and save to remove
it. Credentials are bound to the configured URL: changing the target requires
entering a key for that target, so the old key is not sent to a different instance.
There is no API-key environment fallback. Do not paste the key into chat, put it
in the workspace, or export it to the model's shell.

The model uses `talus_request` with a required `path` and optional `method`,
`json`, `headers`, and `timeout_ms`. It returns `status`, `headers`, and a string
`body`; text/JSON request and response bodies are limited to 1 MiB. The deadline
defaults to 30000 ms and can be set up to 300000 ms. The host obtains the saved
key and adds the existing `X-API-Key` header only to the configured HTTPS
destination. TLS protects the header in transit; redirects are not followed.
No new signing protocol or
Talus backend authentication change is required.

All binary uploads and downloads use the bundled Node helper's `--data-file`
and `--output` options; the host tool does not accept local file paths. The helper
connects through a local Unix-domain socket or Windows named pipe and also
handles larger JSON/text bodies. On Windows, the named pipe and descriptor rely
on inherited Windows ACLs; the plugin does not set or verify a dedicated pipe
ACL. Restrict the account and temporary directory to trusted users. Shell tools receive only
`DSH_TALUS_BROKER` and `DSH_TALUS_HELPER` paths, never an injected API key. The
skill maps these paths to `TALUS_BROKER` and `TALUS_REQUEST` for each shell call.

## Credential storage and migration

The key is saved through dsh's credentials service. After upgrading an older
installation, enter the API key in the plugin form once: legacy configuration
keys and API-key environment variables are ignored. Saving stores the entered
key in the credentials service and clears the legacy configuration field.
The default `credentials-local` provider uses local files (`0600` on Unix);
Windows access depends on the files' ACLs. It is not an OS-encrypted keychain or a strong boundary between
processes running as the same user. This plugin avoids handing the key to the
model, its shell environment, or command arguments. If the model can freely read
that user's credential directory, stronger isolation needs an OS keychain-backed
provider or separate OS accounts and permissions.

For an existing HTTP instance, configure HTTPS before saving its URL here. An old
HTTP setting may remain visible for editing, but all requests reject it.
`TALUS_URL` / `TALUS_API_KEY` and direct authenticated `curl` calls belong to the
older external-client workflow; this plugin and its skill no longer use them.
Configure the plugin's host credentials even when using a headless profile.
The Talus deployment `.env` does not configure this plugin.

## Verify

Confirm that the available skills include `talus`, then ask:

> Use the talus skill and talus_request to list my servers. Make a real request
> and report their names and status.

This task requires `servers:read`. For a service-only key, ask to list registered
services and read a service's usage guide instead. Installation and skill loading
do not by themselves verify a live Talus connection.

## Update or remove

Fully stop the target profile before updating. Remove the installed plugin before
adding the updated directory, then restart the profile:

```bash
dsh plugin --profile web remove dsh-plugin-talus
dsh plugin --profile web add ./plugins/dsh
```

These paths assume the Talus repository root; use an absolute path for a standalone
copy. Replace `web` with your profile throughout. To uninstall, run only the
`remove` command. After reinstalling, check the plugin settings and re-enter them
if needed. An older installation needs this update to gain the host broker and
must have its key entered once in the form; later changes only need saving.

## Maintain and distribute

The repository's `skills/talus/` is the maintained source. The sync script copies
it and adds dsh configuration instructions to the bundled skill, keeping the
portable source unchanged. From this plugin directory inside a Talus checkout,
refresh the bundled copy and check it:

```bash
npm ci --ignore-scripts
npm run sync-skills
npm run check-skills
npm test
npm pack
```

`npm test` checks the plugin; it does not validate your deployed Talus instance.
`npm pack`
creates `dsh-plugin-talus-0.1.0.tgz`; this package has not been published to npm.
Install the archive directly (replace the example with its absolute path):

```bash
dsh plugin --profile web add /absolute/path/dsh-plugin-talus-0.1.0.tgz
```

The archive installation installs its runtime dependencies. You can also extract
it, run `npm install --omit=dev --ignore-scripts` inside the resulting `package/`
directory, then install that directory by its absolute path. The archive includes the skill,
[file reference](skills/talus/references/files.md), and
[monitoring reference](skills/talus/references/metrics.md).

The provider loads only this package's bundled `skills/` directory and does not
watch it for changes. Refresh and reinstall the complete plugin when updating
the skill; restart the profile to load the new files.
