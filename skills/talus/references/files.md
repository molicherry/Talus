# File API

All routes below are relative to `/api/v1/servers/{id}/sftp`. Send `X-API-Key`
on every request, including upload recovery calls. Every operation checks
`server_ids`, uses the configured SSH route and account, and requires an enabled
SFTP subsystem. An administrator must confirm first-use/changed SSH host keys
in the Web UI. File operations do not fall back to shell commands.

Normal responses wrap the listed payload in `{"data": ...}`. Errors use
`{"error":{"code":...,"reason":...,"message":...}}` and may include `request_id`.
Downloads and directory archives return binary attachments instead of JSON.

## Permissions and limits

| Scope | Operations |
| --- | --- |
| `files:read` | List, stat, download, read text, directory ZIP |
| `files:write` | Upload, resume/status/discard own upload, mkdir, rename, save text |
| `files:delete` | Delete a file, link, or empty directory |

File scopes are opt-in. An existing key's Exec/terminal permissions do not grant
file API scopes. File scopes also do not grant permissions beyond the SSH account.

- File transfers: at most 64 MiB per file; a directory archive is limited to
  64 MiB of source file content and 2,000 entries.
- Text editing: at most 1 MiB, valid UTF-8 without NUL bytes.
- Resumable upload chunks: at most 1 MiB; tokens expire after 24 hours.
- Directory listings are bounded to 2,000 entries and can return `truncated:true`.
  Do not interpret a truncated listing as a complete inventory.
- Each binary transfer request has a 10-minute budget and 30-second read/write idle limits.
  Serialize operations per server when `429 sftp_busy` is returned.

URL-encode query paths. Paths are literal filesystem paths: do not expand them
through a shell. Use `application/octet-stream` for upload/chunk bodies and
`application/json` for JSON bodies.

## Endpoints

| Method and path | Request | Response payload / behavior |
| --- | --- | --- |
| `GET /entries?path=...` | Default path `.` | `path`, `parent`, `entries`, `truncated` |
| `GET /stat?path=...` | Remote path | `name`, `path`, `kind`, `size`, `mode`, `modified_at` |
| `GET /download?path=...` | Optional `Range` and `If-Range` | Binary; `ETag`, `Accept-Ranges`, `Content-Length`; partial responses also have `Content-Range` |
| `POST /upload?path=...&overwrite=false` | Raw bytes; Content-Length or `size` query | `size`; existing destination preserved by default |
| `POST /mkdir` | `{"path":"...","parents":false}` | `{"success":true}`; `parents:true` creates missing parents |
| `POST /rename` | `{"from":"...","to":"..."}` | `{"success":true}`; destination must not exist |
| `DELETE /entry?path=...` | Remote path | `{"success":true}`; no recursive deletion |
| `GET /archive?path=...` | Remote directory | Streamed ZIP; includes empty directories, skips symbolic links |
| `GET /text?path=...` | Existing text file | `path`, `content`, `version` |
| `PUT /text` | `path`, edited `content`, original `version` | Saved text with its new version; stale version is `409 sftp_conflict` |
| `POST /uploads/start` | `path`, `size`, `sha256`, `overwrite` | Signed `token`, `path`, `size`, `offset`, `sha256`, `expires_at` |
| `POST /uploads/status` | `{"token":"..."}` | Upload metadata and actual `offset`; `completed:true` if already published |
| `PUT /uploads/chunk?offset=...&size=...` | Raw bytes and `X-Upload-Token` | Upload metadata with confirmed `offset` |
| `POST /uploads/complete` | `{"token":"..."}` | `{"success":true}` after checksum verification and publication |
| `DELETE /uploads` | `X-Upload-Token` header | `{"success":true}`; discards this caller's temporary upload |

Entries have `kind` equal to `file`, `directory`, `symlink`, or `other`.
Metadata alone does not establish content identity; use a text version or
download ETag to detect concurrent changes. File reads/writes do not follow
symlinks as regular files. Removal can remove a link itself.

## Browse and upload examples

Use the configured `TALUS_URL` and `TALUS_API_KEY` from the main skill. Select the
server first and set its ID; example remote paths must match the user's target.

```bash
TALUS_SERVER_ID=1

curl --silent --show-error --fail-with-body --get \
  "${TALUS_URL%/}/api/v1/servers/$TALUS_SERVER_ID/sftp/entries" \
  -H "X-API-Key: $TALUS_API_KEY" --data-urlencode 'path=/home/deploy'
```

Upload an existing local `data.bin`, preserving an existing remote destination:

```bash
curl --silent --show-error --fail-with-body \
  "${TALUS_URL%/}/api/v1/servers/$TALUS_SERVER_ID/sftp/upload?path=%2Fhome%2Fdeploy%2Fdata.bin&overwrite=false" \
  -H "X-API-Key: $TALUS_API_KEY" -H 'Content-Type: application/octet-stream' \
  --data-binary @data.bin
```

Do not convert file bytes to JSON or base64. A `409 sftp_exists` calls for a
decision about the intended destination, not an automatic overwrite retry.

## Edit text with a version check

1. `GET /text?path=...` and retain the returned `data.path`, `data.content`, and
   `data.version` (`sha256:...`). Keep a copy of the original text for comparison.
2. Edit the content locally. Serialize a JSON body containing that exact path,
   the edited content, and the original version. Use a JSON encoder so quotes,
   backslashes, and newlines in the text remain valid; do not interpolate raw text
   into a shell-built JSON string.
3. `PUT /text` with that body and `Content-Type: application/json`.
4. On `409 sftp_conflict`, preserve the draft, fetch the latest remote text, and
   reconcile the changes. Use the new version only after reconciliation. On
   success, keep the returned new version for the next edit.

## Resume an upload

1. Compute the full local file's byte size and SHA256 hex digest. Keep the source
   file unchanged for the transfer. Start with
   `POST /uploads/start {"path":"...","size":123,"sha256":"<64 hex digits>","overwrite":false}`.
2. Keep `data.token` private and retain it for recovery. Tokens bind the caller
   identity, server, SSH route, destination, size, checksum, and expiry.
3. Send bytes beginning at the confirmed offset using
   `PUT /uploads/chunk?offset=<offset>&size=<chunk bytes>` with both `X-API-Key`
   and `X-Upload-Token`. Each chunk is at most 1 MiB. If Content-Length is set,
   it must match `size`. Advance from the returned offset.
4. After a timeout, disconnect, or offset conflict, call
   `POST /uploads/status {"token":"..."}` before sending more bytes. Some bytes
   may already have arrived; continue from the returned offset, not a guessed one.
5. When the offset equals the total size, call
   `POST /uploads/complete {"token":"..."}`. It verifies size/checksum and
   publishes the file. If the final acknowledgement is lost, query status;
   `completed:true` means publication succeeded. Completion is safe to retry.
6. To abandon the transfer, call `DELETE /uploads` with `X-Upload-Token` and the
   same caller's authentication.

Every recovery request authenticates again. Replacing the API key, changing the
SSH route/credentials, or token expiry can prevent recovery. Report the error and
start a new authorized transfer if necessary; possession of the token does not
replace caller authentication. An overwrite request still checks that the
destination has not changed since the upload began.

## Download and resume

Download into a temporary local file and save the response headers:

```bash
curl --silent --show-error --fail --get \
  "${TALUS_URL%/}/api/v1/servers/$TALUS_SERVER_ID/sftp/download" \
  -H "X-API-Key: $TALUS_API_KEY" --data-urlencode 'path=/home/deploy/data.bin' \
  --dump-header download.headers --output data.bin.part
```

Check curl's exit status before considering the file complete. The ETag is a
quoted `sha256:<hex digest>` for the full file. Save it for content verification
and recovery.

To resume, send `Range: bytes=<saved byte count>-` and `If-Range: <saved ETag>`.
Save the new body separately until its status is known:

- On `206`, verify `Content-Range` begins at the requested offset and the ETag
  matches, then append the received bytes.
- On `200`, replace the old partial file with the new full body; do not append it.
- On `416 sftp_invalid_range`, inspect the saved length and remote version before
  deciding whether the file is complete or a fresh download is needed.

Check final byte count and SHA256 against the response metadata before publishing
the local file. A remote read can fail after headers; HTTP 200 alone does not
prove the body completed. Check ZIP integrity for directory archive downloads.

## Transfer directories

Download a remote directory with `GET /archive?path=...`. It includes regular
files and empty directories, skips links, and enforces the archive limits above.
ZIP downloads do not use the single-file resume endpoint.

To upload a directory, enumerate the intended local tree, create its directories
with `POST /mkdir` (`parents:true` where needed), then upload each regular file
with `/upload` or `/uploads/*`. Preserve relative paths and empty directories;
do not silently traverse symlinks. There is no directory-upload or ZIP-extraction
endpoint, and no recursive-delete endpoint.

## Recoverable errors

| Error | Action |
| --- | --- |
| `403 sftp_permission_denied` | Report the SSH account's permission failure; do not escalate privileges. |
| `409 sftp_conflict` | Reload the version/status and reconcile; retain local work. |
| `409 sftp_exists` / `sftp_not_empty` | Inspect the destination; do not automatically overwrite or delete a tree. |
| `410 sftp_upload_expired` | The token cannot resume; start a new transfer if still needed. |
| `413 sftp_too_large` / `sftp_directory_too_large` / `sftp_text_too_large` | Respect the file/archive/text limit; choose a smaller authorized transfer. |
| `429 sftp_busy` | Wait with backoff and serialize operations on that server. |
| `502 sftp_unavailable` | Have the administrator enable/fix SFTP; do not fall back to Exec. |
| `sftp_checksum_mismatch` / `sftp_size_mismatch` | Verify the local source, byte count, and transfer status before retrying. |
