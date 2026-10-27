# Windows Server monitoring

Talus includes a native Windows amd64 agent. The Hub installs it through
OpenSSH/SFTP, then an automatic Windows service reports over outbound WSS/HTTPS.
Normal monitoring uses no SSH sessions and requires no additional inbound port.

The initial targets are Windows Server 2019, 2022 and 2025 x64. Native CI covers
2022 and 2025; 2019 still needs deployment-specific acceptance. ARM64 and 32-bit
Windows are outside this initial scope. Metrics include CPU, memory, paging
files, disk capacity/I/O, network traffic, OS information and uptime. Linux load
averages are explicitly not applicable. IIS, SQL Server and Windows service
inventory are not collected. This feature does not establish full Windows
compatibility for the browser terminal or file browser.

## Setup

1. Enable Windows OpenSSH Server and its SFTP subsystem. Allow the Hub's SSH
   connection; cmd.exe and PowerShell are supported as the OpenSSH default shell.
2. Add an administrator SSH credential and server in Talus, verify the host key,
   and select any required SSH jump/SOCKS5 route. Installation cannot elevate an
   unprivileged login interactively.
3. In **System settings → Monitoring**, set the HTTPS Hub URL reachable by the
   Windows service. Deployment-time `AGENT_HUB_URL` remains supported. The sample
   interval follows the same settings as Linux.
4. Ensure Windows PowerShell 5.1+, the running Task Scheduler (`Schedule`), and
   a writable local ProgramData directory are available. The Hub certificate
   must be trusted by LocalSystem; install a private CA in the local machine's
   trusted root store when needed.
5. Wait for monitoring to report ready/online. Agent traffic uses WSS with HTTPS
   fallback independently of the SSH connection route.

## Lifecycle

The service is `Talus-Agent-<installation ID>`, running as LocalSystem with
automatic startup and failure recovery. Files live in
`%ProgramData%\Talus-Agent\<installation ID>`. Directory ACLs restrict access to
SYSTEM and local Administrators before any token is uploaded. Windows SFTP's
missing POSIX chmod is not treated as a permission mechanism.

Installation and updates verify SHA-256 before activation. A temporary scheduled
task performs the switch independently of SSH, verifies authenticated Hub
contact, and restores the previous executable/configuration on failure. The
outbox retains the existing 24-hour/64-MiB bounds. Windows file locking excludes
concurrent writers; the kernel boot GUID prevents rate calculations across boots.

Deleting a server schedules independent local cleanup of its service, deployment
tasks and installation directory using an administrator credential. Cleanup waits
for any activation holding the deployment lock. If SSH is unreachable, the Hub
revokes the node credential but remote files/services need manual cleanup. Switching SSH administrators does
not move the machine-wide installation. A stable deployment lock remains outside
the removed directory to serialize pending activations and removal.

If deployment fails, check administrator privileges, SFTP, Task Scheduler,
ProgramData access, outbound HTTPS/WSS and machine-wide certificate trust. Failed
metrics remain missing with a reported collection error instead of fabricated
zero values.

## Native verification

From an administrator PowerShell in `backend`:

```powershell
$env:TALUS_WINDOWS_INTEGRATION='1'
go test -v -count=1 ./internal/agentclient ./internal/agentwire ./internal/agentwindows
```

This creates temporary services/tasks, executes the actual agent, checks metrics,
configuration upgrades, failed-contact rollback and idempotent removal, then
cleans up. Cross-compilation and Linux SSH fixtures do not replace native tests.
The artifact build includes Linux amd64/arm64 and Windows amd64; startup verifies
each manifest entry, size, SHA-256 and ELF/PE architecture.

## Removal receipts and retries

The first removal attempt uses `Lock-Install 200`, waiting up to **200 seconds**
and checking every 100 milliseconds. Subsequent attempts wait at most 60, 30 and
10 seconds for the lock: 300 seconds in total, leaving room for service shutdown
and directory deletion. After committing deletion, the Hub briefly checks the
receipt and logs pending cleanup at Info. Scheduling failures, unavailable
receipts and failed removal are logged at Warn. A failed task retains its script,
receipt and `Talus-Agent-<installation ID>-Remove` task. The detached SYSTEM task retries up to three
additional times, waiting one minute after each failure, with a twenty-minute
limit for the entire task. Failures
also produce a Windows Application event from `Talus-Agent`.

Use administrator PowerShell with the actual installation ID and original
ProgramData location:

```powershell
$installId = '<installation ID>'
$root = Join-Path $env:ProgramData 'Talus-Agent'
$name = 'Talus-Agent-' + $installId
Get-Content -LiteralPath (Join-Path $root ($name + '.remove.json'))
Get-ScheduledTaskInfo -TaskName ($name + '-Remove')
```

Receipt status is `pending`, `failed` or `succeeded`, with an error, attempt count and timestamp
on failure. After resolving locked files or service-stop failures, retry with
`Start-ScheduledTask -TaskName ($name + '-Remove')`. Success removes the task and
script but retains the receipt. If execution was forcibly terminated before a
receipt could be written, also check Task Scheduler's last result. The Hub does
not keep polling deleted nodes after SSH disconnects or request cleanup ends.

Activation has a 180-second task limit. The Hub waits 200 seconds for its receipt
and allows another 10 seconds for SSH completion. Deployment jobs have a
twelve-minute overall budget including uploads. Before launching activation, the
Hub requires enough remaining time for launch, the full receipt wait, persistence
and SSH cleanup; otherwise it retries without starting a detached task. External
cancellation or connection loss can end the Hub wait earlier; the local task
continues activation or rollback.

The bootstrap downloads binaries with the built-in HTTP client and reuses a
digest-keyed cache under the installation directory. Partial downloads resume
with Range/If-Range; cached and downloaded bytes are checked with SHA256. The
cache inherits SYSTEM/Administrators-only ACLs. Download scripts are uploaded as
UTF-8 files with a BOM and verified before execution, avoiding cmd.exe's command
length limit; credentials stay in a separate protected file removed by the
download script. An unavailable HTTP path can fall back to SFTP; HTTP 429/503
and interrupted pulls with new bytes retain progress and retry after thirty
seconds. Reads are bounded by both an idle limit and the remaining transfer
budget so progress can be reported before SSH cancellation. See
[Monitoring agent upgrades](../README.md#monitoring-agent-upgrades) for shared
transfer, queue and retry rules.

## Changing operating system or ProgramData

Tracked platforms and directories must remain consistent so old services and
failed installations remain discoverable for cleanup. Before switching Linux to
Windows or relocating ProgramData, delete the original server in Talus while its
old system/path is accessible, confirm remote cleanup, then change the system or
path and add the server again. This creates new server and installation IDs;
back up any data you need to retain first.

If the change already happened, restore administrative access to the old
installation and clean up its services/tasks before deleting and re-adding the
record. Do not bypass checks by editing pending/completed layout fields directly
in the database. Changing SSH administrators does not move this machine-wide
installation.

Windows disk capacity sums readable drive-letter roots, skipping inaccessible
or unlettered volumes; having no readable disk is still an error. Timed-out
collection children get a ten-second reaping grace period. Each section permits
at most one stuck old worker and one replacement. If both remain stuck, it keeps
reporting timeouts until the OS operation recovers or the agent restarts, rather
than accumulating processes indefinitely.
