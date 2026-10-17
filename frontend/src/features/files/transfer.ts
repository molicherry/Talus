import { ApiClientError } from "../../lib/api-client";
import * as files from "./api";
const aborted = (signal: AbortSignal) => { if (signal.aborted) throw new DOMException("Aborted", "AbortError"); };
const failure = (reason: string) => new ApiClientError(409, { error: { code: 409, reason, message: "" } });
export async function resumableUpload(id: number, path: string, file: File, overwrite: boolean, options: {
  signal: AbortSignal; onProgress: (loaded: number, total: number) => void;
  onSession: (session: files.UploadSession | null) => void; session?: files.UploadSession | null;
}) {
  if (file.size > files.MAX_FILE_BYTES) throw failure("sftp_too_large");
  const digest = await files.sha256(file, options.signal); aborted(options.signal);
  let session = options.session;
  if (session) {
    if (session.size !== file.size || session.sha256 !== digest) throw failure("sftp_checksum_mismatch");
    const token = session.token!;
    session = { ...await files.uploadStatus(id, token, options.signal), token };
  } else session = await files.startUpload(id, path, file.size, digest, overwrite, options.signal);
  aborted(options.signal); options.onSession(session);
  const uploadToken = session.token;
  if (!uploadToken) throw failure("sftp_invalid_upload");
  if (session.completed) { options.onSession(null); return; }
  let offset = session.offset;
  options.onProgress(offset, file.size);
  while (offset < file.size) {
    aborted(options.signal);
    const chunk = file.slice(offset, offset + 1024 * 1024);
    const result = await files.uploadChunk(id, uploadToken, offset, chunk, { signal: options.signal, onProgress: loaded => options.onProgress(offset + loaded, file.size) });
    aborted(options.signal);
    if (result.offset !== offset + chunk.size) throw failure("sftp_conflict");
    offset = result.offset; session = { ...session, offset }; options.onSession(session);
  }
  await files.completeUpload(id, uploadToken, options.signal); aborted(options.signal);
  options.onSession(null);
}
export interface DownloadSession { path: string; name: string; size: number; offset: number; etag: string | null; parts: Blob[] }
export async function resumableDownload(id: number, session: DownloadSession, signal: AbortSignal, onProgress: (loaded: number, total: number) => void): Promise<Blob> {
  const chunkSize = 16 * 1024 * 1024;
  if (session.size > files.MAX_FILE_BYTES) throw failure("sftp_too_large");
  while (session.offset < session.size || session.parts.length === 0) {
    aborted(signal);
    const offset = session.offset;
    let status = 0, headers = new Headers();
    const blob = await files.download(id, session.path, { signal,
      headers: session.size ? { Range: `bytes=${offset}-${Math.min(offset + chunkSize, session.size) - 1}`, ...(session.etag ? { "If-Range": session.etag } : {}) } : {},
      onProgress: loaded => onProgress(offset + loaded, session.size), onHeaders: (h, s) => { headers = h; status = s; },
    });
    aborted(signal);
    const etag = headers.get("ETag");
    if (status === 200) { session.parts = [blob]; session.offset = blob.size; session.size = blob.size; session.etag = etag; break; }
    const range = headers.get("Content-Range")?.match(/^bytes (\d+)-(\d+)\/(\d+)$/);
    if (status !== 206 || !range || Number(range[1]) !== offset || blob.size !== Number(range[2]) - offset + 1 || Number(range[3]) > files.MAX_FILE_BYTES || (session.etag && session.etag !== etag)) throw failure("sftp_conflict");
    session.parts.push(blob); session.offset += blob.size; session.size = Number(range[3]); session.etag = etag;
    onProgress(session.offset, session.size);
  }
  const result = new Blob(session.parts, { type: "application/octet-stream" });
  if (session.etag?.startsWith('"sha256:')) {
    const digest = await files.sha256(result, signal); aborted(signal);
    if (`"sha256:${digest}"` !== session.etag) { session.parts = []; session.offset = 0; session.etag = null; throw failure("sftp_checksum_mismatch"); }
  }
  return result;
}
export function relativeUploadPath(value: string): string {
  const segments = value.split("/");
  if (!value || segments.some(part => !part || part === "." || part === ".." || part.includes("\\") || part.includes("\0"))) throw failure("sftp_invalid_path");
  return segments.join("/");
}
