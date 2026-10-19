# Resident monitoring

Talus installs a resident Linux agent over the server's configured SSH/SFTP
route. Normal sampling and delivery use outbound WSS to the Hub. When WebSocket
upgrade is unavailable, the agent uses HTTPS POST and periodically retries WSS.
The previous SSH polling scheduler is no longer started by the Hub. The one-shot
agent command remains available for artifact verification and older tooling.

## Configuration and upgrade

Set `AGENT_HUB_URL` to the Hub address reachable **from the managed nodes**, for
example `https://talus.example.com`. An optional path prefix is supported. Set
`MONITOR_INTERVAL` to 1–60 seconds (default 5). Explicit `AGENT_ALLOW_HTTP=true`
allows HTTP for a trusted test/private network; certificate verification remains
enabled for HTTPS/WSS. Agent credentials are never sent in URLs or redirects.

Existing databases migrate automatically. Historical metrics remain available.
Without `AGENT_HUB_URL`, the Hub starts and the UI shows **Monitoring setup
required** rather than continuing SSH polling or pretending existing metrics
establish a live connection. Configure the address before rolling out this Hub.

Nodes need Linux amd64/arm64, SSH/SFTP for installation, and a running systemd
manager. Root SSH accounts install system services in `/etc/systemd/system`, with
binary, configuration and outbox under `/var/lib/talus-agent/<installation-id>`.
Other SSH accounts install a systemd user service and use
`~/.local/share/talus-agent/<installation-id>`. For user services, an administrator
must enable lingering (`loginctl enable-linger <ssh-user>`) so the service survives
logout and starts on boot. Talus does not run sudo or elevate the SSH account.
Missing service permissions produce a visible deployment error and an automatic
retry; they do not trigger an alternate connection route.

The Hub reconciles installations every 15 seconds, with at most four deployments
in flight and a three-minute deployment budget. Successful healthy installations
do not consume monitoring SSH sessions. Bundled artifacts, uploaded configuration
and service files are verified by SHA256 before activation. New releases/configs
are staged beside the current binary. A service must demonstrate authenticated
contact with the Hub after restart; failed activation restores the prior binary,
configuration and unit where available. A failed first installation stops the
new service. Deployment failures retry after at least one minute.

## Collection and delivery guarantees

- The default sampling interval is 5 seconds, the application heartbeat interval
  is 15 seconds, and contact older than 45 seconds is unreachable. Reconnect uses
  exponential backoff with jitter, capped at 30 seconds. These are scheduling
  budgets, not latency guarantees for a failed network.
- Collectors run in isolated local child processes with a two-second deadline.
  A stuck collector retains at most one outstanding worker for that section;
  subsequent rounds report it unavailable and continue collecting other metrics.
  CPU is calculated from cumulative counters between samples. Hardware metadata
  refreshes every ten minutes. Unsupported/failed metrics stay null.
- A sample is atomically written and synced into a private local outbox before
  delivery. Only committed ACKs remove it. The outbox survives agent/host restarts
  and is protected against concurrent agent processes.
- Retention is bounded by **24 hours and 64 MiB**, whichever is reached first.
  Oldest samples are discarded with a persistent loss counter. A full or failing
  local disk is reported as a buffer error; it cannot provide durable storage.
- Recovery sends the newest observation first, then batches the oldest backlog
  (at most 32 samples / 1 MiB per message). Receipts and metric rows commit in one
  PostgreSQL transaction. Replaying a lost ACK does not duplicate observations,
  including after a Hub restart. Failed writes return no ACK.
- Historical observations retain their capture time and never replace a newer
  live snapshot. Boot IDs prevent rate calculations across host reboots, even
  when the new boot's cumulative counter already exceeds the old one.
- Nodes must maintain a reasonably synchronized clock. The Hub rejects samples
  more than 25 hours old or five minutes in the future, without blocking valid
  samples in the same batch or hiding authenticated contact. Explicit rejections
  are removed from the outbox and counted as dropped, and clock errors appear in
  the UI. Correcting the clock restores normal collection. Receipts are retained
  for 48 hours, beyond the accepted replay horizon.

## Status and credentials

The UI separates authenticated contact, collector health, and persisted data
freshness. Database write failure does not mark an established agent offline.
It shows pending/backfilling samples, dropped samples, collector errors, deployment
errors, agent version, last contact, last sample and last successful write.
The UI polls every five seconds. Restarted Hubs await fresh contact rather than
deriving online status from historical data.

Every node receives a separate random credential, encrypted at rest with a
purpose-scoped key. It grants access only to that node's monitoring endpoint.
Deleting a server revokes its credential and disconnects its current WebSocket.
Replacing its host/port/trusted host key rotates the credential; changing only an
SSH route or login credential preserves healthy monitoring. Removed servers'
agents can be stopped and disabled on the old host with
`systemctl disable --now talus-agent-<installation-id>.service` (add `--user` for
user services). Removal does not remotely delete the retained local outbox.

## Validation

Unit/integration coverage exercises durable restart recovery, lost ACK replay,
bounded retention, single-process ownership, local collector timeout isolation,
HTTP fallback and WebSocket recovery, replacement connection ownership, write
failure without false offline status, concurrent SQL deduplication, transaction
rollback, credential rotation/revocation, reboot rate boundaries, and executable
activation/rollback with a controlled service manager. PostgreSQL tests use an
isolated schema via `TEST_DATABASE_URL`. Browser tests exercise the status UI.
