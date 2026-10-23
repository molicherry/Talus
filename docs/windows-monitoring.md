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
