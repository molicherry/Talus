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

## Configure the execution environment

Set `TALUS_URL` to the instance base URL, without `/api/v1`. If unset, the skill
uses `http://localhost:8080`; `localhost` refers to the environment running dsh.
Obtain `TALUS_API_KEY` from **Talus Web UI → API Keys**. For a Bash launch:

```bash
export TALUS_URL='https://talus.example.com'
read -r -s -p 'Talus API key: ' TALUS_API_KEY
printf '\n'
export TALUS_API_KEY
```

Start dsh from that environment so its request tools inherit the variables.
For persistent, Desktop, or remote execution, configure the environment or secret
settings of the process that actually runs requests, then restart it. An already
running Desktop app does not inherit later shell exports. The Talus server's
deployment `.env` does not configure dsh. Keep API keys out of plugin files and chat.

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

Confirm that the available skills include `talus`, then ask:

> Use the talus skill and the configured environment to list my servers. Make a
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
`remove` command. Instance configuration stays in the execution environment.

## Maintain and distribute

The repository's `skills/talus/` is the maintained source. From this plugin
directory inside a Talus checkout, refresh the bundled copy and check it:

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
