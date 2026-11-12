# Talus for DeepSeek Harness

English | [简体中文](README.zh-CN.md)

`dsh-plugin-talus` 0.1.0 bundles the [Talus skill](skills/talus/SKILL.md) for
DeepSeek Harness (dsh). It uses dsh's official filesystem skill provider to load
server management, SSH execution, monitoring, SFTP, and service relay guidance.
The complete skill and its references are included; an installed plugin does not
need the Talus source repository.

## Requirements

- Node.js 24.9.0 or later and DSH 0.2.2-rc.1. The package's `engines.dsh` range is
  `~0.2.2-rc.1`; dsh's plugin API is still in preview.
- A running Talus instance reachable from dsh's execution environment.
- A Talus API key with scopes for the intended tasks, and `curl` or an equivalent
  HTTP tool available to dsh. See the bundled [permission guide](skills/talus/SKILL.md#authentication-and-permissions).

## Install

From the Talus repository root, install into the Web profile:

```bash
dsh plugin --profile web add ./plugins/dsh
```

For an independent copy of this plugin directory, use its absolute path instead
of `./plugins/dsh`. The third-party plugin interface also accepts a local absolute
directory. Restart the selected profile after installation.

Use `--profile headless` for a headless profile. For Desktop, launch Desktop once
to initialize its profile, fully quit it, then install with `--profile desktop`
and relaunch. Installing into `web` does not install into the other profiles.

## Configure in the plugin page

Open **Plugins → Talus** in dsh. Enter the instance base URL, such as
`https://talus.example.com`, without `/api/v1`, and the API key from
**Talus Web UI → API Keys**, then save. Saved settings apply to subsequent
requests without reinstalling the plugin or restarting dsh.

The API key uses a password field and is never filled back into the form. Leave
it blank to retain the saved key, or choose **Clear saved API key** and then save
to remove it.
Each field uses the plugin's saved value first, then `TALUS_URL` or
`TALUS_API_KEY` from the execution environment; the URL ultimately defaults to
`http://localhost:8080`. Clearing the saved key restores the environment fallback
if `TALUS_API_KEY` is set. `localhost` refers to the environment running dsh.

DSH's native configuration editor stores these settings in the current profile's
private `cordis.patch.yml` (mode `0600`), redacts the key in remote settings reads,
and does not provide an additionally encrypted vault.

### Environment variables for headless or existing setups

For fields not configured in the plugin, the original environment variables
remain supported. For a Bash launch:

```bash
export TALUS_URL='https://talus.example.com'
read -r -s -p 'Talus API key: ' TALUS_API_KEY
printf '\n'
export TALUS_API_KEY
```

Start dsh from that environment so it inherits the variables. An already running
Desktop app does not inherit later shell exports; restart it with the intended
environment or use the plugin form. The Talus server's deployment `.env` does not
configure dsh. Do not put API keys in the plugin source or paste them into chat.

## Verify

Confirm that the available skills include `talus`, then ask:

> Use the talus skill and my saved configuration to list my servers. Make a
> real API request, report their names and status, and do not display the API key.

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
if needed. An older installation needs this update once to gain the configuration
form; subsequent settings changes only need saving.

## Maintain and distribute

The repository's `skills/talus/` is the maintained source. The sync script copies
it and adds dsh configuration instructions to the bundled skill, keeping the
portable source unchanged. From this plugin directory inside a Talus checkout,
refresh the bundled copy and check it:

```bash
npm ci
npm run sync-skills
npm run check-skills
npm test
npm pack
```

`npm test` exercises registration and skill loading with Cordis and dsh's real
filesystem skill provider; it does not connect to a Talus instance. `npm pack`
creates `dsh-plugin-talus-0.1.0.tgz`; this package has not been published to npm.
Install the archive directly (replace the example with its absolute path):

```bash
dsh plugin --profile web add /absolute/path/dsh-plugin-talus-0.1.0.tgz
```

You can also extract it and install the resulting `package/` directory by its
absolute path. The archive includes the skill,
[file reference](skills/talus/references/files.md), and
[monitoring reference](skills/talus/references/metrics.md).

The provider loads only this package's bundled `skills/` directory and does not
watch it for changes. Refresh and reinstall the complete plugin when updating
the skill; restart the profile to load the new files.
