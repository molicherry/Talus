import { sha256 as sha256Hash } from "@noble/hashes/sha2.js";
import { z } from "zod";
import { apiClient, type FileTransferOptions } from "../../lib/api-client";

export const MAX_FILE_BYTES = 64 * 1024 * 1024;
const EntrySchema = z.object({
  name: z.string(), path: z.string(), kind: z.enum(["file", "directory", "symlink", "other"]),
  size: z.number().nonnegative(), mode: z.string(), modified_at: z.string(),
});
const DirectorySchema = z.object({ path: z.string(), parent: z.string(), entries: z.array(EntrySchema), truncated: z.boolean() });
export type FileEntry = z.infer<typeof EntrySchema>;
export type FileDirectory = z.infer<typeof DirectorySchema>;
const base = (id: number) => `/api/v1/servers/${id}/sftp`;
const query = (path: string) => new URLSearchParams({ path }).toString();
export async function listFiles(id: number, path: string, signal?: AbortSignal) {
  return DirectorySchema.parse(await apiClient.get(`${base(id)}/entries?${query(path)}`, { signal }));
}
export const mkdir = (id: number, path: string, signal?: AbortSignal) => apiClient.post(`${base(id)}/mkdir`, { path }, { signal });
export const rename = (id: number, from: string, to: string, signal?: AbortSignal) => apiClient.post(`${base(id)}/rename`, { from, to }, { signal });
export const remove = (id: number, path: string, signal?: AbortSignal) => apiClient.delete(`${base(id)}/entry?${query(path)}`, { signal });
export const upload = (id: number, path: string, file: File, overwrite: boolean, options: FileTransferOptions) =>
  apiClient.transfer(`${base(id)}/upload?${query(path)}&size=${file.size}&overwrite=${overwrite}`, { ...options, method: "POST", body: file });
export const download = (id: number, path: string, options: FileTransferOptions) =>
  apiClient.transfer<Blob>(`${base(id)}/download?${query(path)}`, { ...options, download: true });
export function saveDownload(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a"); anchor.href = url; anchor.download = name;
  document.body.append(anchor); anchor.click(); anchor.remove();
  // Keep the URL alive until the browser has consumed the download action.
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}
export const joinRemote = (parent: string, name: string) => `${parent.replace(/\/$/, "")}/${name}`;
export function fileSize(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}

const UploadSchema = z.object({ token: z.string().optional(), path: z.string(), size: z.number().nonnegative(), offset: z.number().nonnegative(), sha256: z.string(), expires_at: z.string(), completed: z.boolean().optional() });
const TextSchema = z.object({ path: z.string(), content: z.string(), version: z.string() });
export type UploadSession = z.infer<typeof UploadSchema>;
export type RemoteText = z.infer<typeof TextSchema>;
export const readText = async (id: number, path: string, signal?: AbortSignal) => TextSchema.parse(await apiClient.get(`${base(id)}/text?${query(path)}`, { signal }));
export const saveText = async (id: number, value: RemoteText, signal?: AbortSignal) => TextSchema.parse(await apiClient.put(`${base(id)}/text`, value, { signal }));
export const mkdirParents = (id: number, path: string, signal?: AbortSignal) => apiClient.post(`${base(id)}/mkdir`, { path, parents: true }, { signal });
export const archive = (id: number, path: string, options: FileTransferOptions) => apiClient.transfer<Blob>(`${base(id)}/archive?${query(path)}`, { ...options, download: true });
export const startUpload = async (id: number, path: string, size: number, sha256: string, overwrite: boolean, signal?: AbortSignal) => UploadSchema.parse(await apiClient.post(`${base(id)}/uploads/start`, { path, size, sha256, overwrite }, { signal }));
export const uploadStatus = async (id: number, token: string, signal?: AbortSignal) => UploadSchema.parse(await apiClient.post(`${base(id)}/uploads/status`, { token }, { signal }));
export const uploadChunk = async (id: number, token: string, offset: number, chunk: Blob, options: FileTransferOptions) => UploadSchema.parse(await apiClient.transfer(`${base(id)}/uploads/chunk?offset=${offset}&size=${chunk.size}`, { ...options, method: "PUT", body: chunk, headers: { "X-Upload-Token": token } }));
export const completeUpload = (id: number, token: string, signal?: AbortSignal) => apiClient.post(`${base(id)}/uploads/complete`, { token }, { signal });
export const cancelUpload = (id: number, token: string, signal?: AbortSignal) => apiClient.delete(`${base(id)}/uploads`, { signal, headers: { "X-Upload-Token": token } });
export async function sha256(data: Blob, signal?: AbortSignal): Promise<string> {
  const hash = sha256Hash.create();
  const reader = data.stream().getReader();
  const abort = () => { void reader.cancel().catch(() => {}); };
  signal?.addEventListener("abort", abort, { once: true });
  try {
    while (true) {
      if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
      const { value, done } = await reader.read();
      if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
      if (done) break;
      hash.update(value);
    }
    return Array.from(hash.digest(), value => value.toString(16).padStart(2, "0")).join("");
  } finally { signal?.removeEventListener("abort", abort); reader.releaseLock(); }
}

export function readPendingUpload(key: string): UploadSession | null {
  try { const parsed = UploadSchema.parse(JSON.parse(sessionStorage.getItem(key) ?? "null")); return parsed.token ? parsed : null; } catch { return null; }
}
export function storePendingUpload(key: string, session: UploadSession | null) {
  try { if (session) sessionStorage.setItem(key, JSON.stringify(session)); else sessionStorage.removeItem(key); } catch { /* Current-tab state still works when storage is unavailable. */ }
}
