# SFTP files and API

Talus file management uses the configured SSH account and SSH port. It reuses encrypted credentials, explicit host-key confirmation, direct/SOCKS5/jump routing, connection revisions and pool quotas. The target must enable its SSH `sftp` subsystem. No additional FTP server or target agent is required. These operations have the filesystem permissions of that SSH account; the API does not add a directory jail or `sudo` access.

The workflow is informed by [EdgeSSH](https://github.com/aozorae/EdgeSSH): a file panel alongside the terminal, binary transfers, progress/cancellation and temporary-file publication. Talus implements the protocol through `github.com/pkg/sftp` on its Go backend.

## Interface

The terminal, file panel and text editor follow the global light/dark/system theme. Theme changes preserve the SSH session, terminal output and editor draft.

Open **Files** in the SSH toolbar. Desktop uses a split pane; phones switch between the terminal and files without reconnecting the terminal. Browse by path, upload or download files, create directories, rename/move entries, and delete files or empty directories. Upload replacement requires confirmation. New files use mode `0600`; replacement preserves the existing permission bits. The owner of a replacement file is the SSH account creating the temporary file.

Uploads use 1 MiB chunks and a SHA256 checksum of the complete file. Cancel pauses a resumable upload. Re-select the same local file to continue; the backend supplies the actual remote offset. **Discard partial upload** removes its temporary file without deleting the destination. The current tab remembers the upload across page reloads. Signed recovery tokens expire after 24 hours and bind the uploader identity, server, SSH route, destination, size and checksum. They remain usable after a Hub restart when `JWT_SECRET`, credentials and connection configuration remain unchanged. An expired token can still discard its own temporary file when the caller and SSH route match. Abandoned files whose route changed can be removed through normal file management by an account with delete permission.

Downloads use verified ranges. While the file panel stays open, a paused download can resume from the last complete 16 MiB chunk. Closing/reloading the panel releases those browser-held chunks; a new download then starts from the beginning. `If-Range` prevents mixing versions, and the browser verifies the completed download against its SHA256 ETag.

**Upload folder** preserves relative subdirectories and refuses to overwrite existing files automatically. Earlier successful files remain if a later file fails or the transfer is cancelled. Browser folder selection supplies files only, so empty local directories are not uploaded. **Download folder as ZIP** includes nested and empty remote directories, skips symbolic links/special files, and checks limits before starting the response.

**Edit text** supports UTF-8 text up to 1 MiB, preserves line endings and permission bits, and sends a content-hash version on save. A stale version returns `409 sftp_conflict`; the UI keeps the draft. Reloading or closing a dirty editor requires an explicit discard. Files are written through a same-directory temporary file. Because SFTP has no conditional rename, external writers can still race the final version check and rename; the version check is optimistic concurrency control rather than a filesystem-wide lock.

## Limits and lifecycle

- 64 MiB per file and for the combined file contents in a directory ZIP/folder upload; ZIP metadata adds at most 2 MiB. Text editing is limited to 1 MiB.
- Directory responses show at most 2,000 entries or 2 MiB of metadata; directory protocol input is bounded to 8 MiB. ZIP manifests allow 2,000 entries and 32 levels of subdirectories. Oversized directories return a limit error or an explicit truncated listing.
- One active SFTP operation per server, sharing the existing SSH quota; other terminal/monitor leases keep their own transports. A reservation is released only after its SSH channel and workers settle.
- Metadata operations have a 25-second budget. Transfers have a 10-minute total budget and HTTP reads/writes use a 30-second idle deadline. Reverse proxies must also permit these transfer sizes/timeouts.
- Ordinary uploads clean temporary files on handled failures/cancellation where the remote remains reachable. Network loss can leave a temporary file. Resumable uploads intentionally keep it until completion/discard/expiry; the original destination is not touched before commit.
- Overwrite requires `posix-rename@openssh.com`. Talus never deletes the original as an overwrite fallback. New-file publication prefers the atomic hard-link extension where available, otherwise uses the SFTP v3 no-clobber rename contract.
- Symbolic links are displayed and can be renamed/deleted, but file download/edit/overwrite do not intentionally follow them. Directory archives do not follow links. Filesystem isolation, when required, should be enforced by the target SSH account/chroot.

## Authentication and scopes

Use a user JWT (`Authorization: Bearer …`) or an API key (`X-API-Key: …`). All routes enforce the API key's `server_ids`. The new scopes are opt-in; existing keys do not acquire file permissions automatically. A key with Exec or terminal access still has the underlying SSH account's command capabilities.

| Scope | Operations |
| --- | --- |
| `files:read` | List, stat, download, read text, directory ZIP |
| `files:write` | Upload, resume/status/discard own upload, mkdir, rename, save text |
| `files:delete` | Delete a file, link or empty directory |

API keys cannot approve an unverified or changed host key. An administrator must confirm it in the Web UI first. SFTP unavailability is reported explicitly; there is no shell-command fallback.

## API

All paths below are relative to `/api/v1/servers/{id}/sftp`. Send remote paths in a URL-encoded query parameter or a JSON body. Normal responses use `{"data": ...}` and errors use `{"error":{"code":...,"reason":...,"message":...}}`. File and ZIP downloads are binary responses. Transfer bodies are binary, not base64 or JSON.

| Method and path | Request | Response |
| --- | --- | --- |
| `GET /entries?path=…` | Default path `.` | `path`, `parent`, `entries`, `truncated` |
| `GET /stat?path=…` | Remote path | `name`, `path`, `kind`, `size`, `mode`, `modified_at` |
| `GET /download?path=…` | Optional `Range`, `If-Range` headers | Binary, `ETag`, `Accept-Ranges`, `Content-Length`; `206` plus `Content-Range` when partial |
| `POST /upload?path=…&overwrite=false` | Raw bytes; Content-Length or `size` query | Uploaded size; one-shot upload, cancellation cleans partial data where possible |
| `POST /mkdir` | `{"path":"…","parents":false}` | Success; `parents:true` creates missing parents |
| `POST /rename` | `{"from":"…","to":"…"}` | Success; destination must not exist |
| `DELETE /entry?path=…` | Remote path | Deletes file/link or empty directory; no recursive delete |
| `GET /archive?path=…` | Remote directory | Streamed ZIP attachment |
| `GET /text?path=…` | Remote text file | `path`, `content`, `version` |
| `PUT /text` | `{"path":"…","content":"…","version":"sha256:…"}` | Saved text and new version; stale version is `409` |
| `POST /uploads/start` | `{"path":"…","size":123,"sha256":"64 hex digits","overwrite":false}` | Signed `token`, `path`, `size`, `offset`, `sha256`, `expires_at` |
| `POST /uploads/status` | `{"token":"…"}` | Actual offset and metadata; `completed:true` recovers a lost final acknowledgement |
| `PUT /uploads/chunk?offset=…&size=…` | Raw bytes, at most 1 MiB; `X-Upload-Token` header | Confirmed remote offset; stale offset is `409` |
| `POST /uploads/complete` | `{"token":"…"}` | Verifies total size/checksum and publishes; safe to retry after a lost acknowledgement |
| `DELETE /uploads` | `X-Upload-Token` header | Removes this uploader's temporary file |

Each chunk/status/complete request is authenticated again. Resume requires the same JWT user or API-key identity, not merely the same role. Changing the route/credentials makes an old upload token unusable for that new route. If a chunk request fails, query `/uploads/status` before retrying because some bytes may already have arrived.

Directory entries have `kind` equal to `file`, `directory`, `symlink` or `other`. File size and modified time are metadata, not a guarantee against concurrent external writes. Use the content version for editing and `If-Range` for resumed downloads. Downloads may abort after headers when a remote read fails; clients must check Content-Length/checksum or ZIP integrity rather than treating a partial body as success.

Usage logs record the operation, caller, server, outcome, route and bounded byte counts. They do not record file contents, recovery tokens or remote path strings.

### Examples

```bash
# Browse with a file-read API key.
curl --get "$TALUS_URL/api/v1/servers/1/sftp/entries" \
  -H "X-API-Key: $TALUS_API_KEY" --data-urlencode 'path=/home/deploy'

# One-shot binary upload. An existing destination is preserved by default.
curl -X POST "$TALUS_URL/api/v1/servers/1/sftp/upload?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -H 'Content-Type: application/octet-stream' \
  --data-binary @data.bin

# Download or resume with a file-read key. Retain/use the ETag with If-Range
# when the remote file might change between requests.
curl "$TALUS_URL/api/v1/servers/1/sftp/download?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -o data.bin
```
