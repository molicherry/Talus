import { ArrowUp, Download, File, FileQuestion, Folder, FolderPlus, Link2, Loader2, Pencil, RefreshCw, Trash2, Upload, X, FilePenLine } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../../hooks/use-auth";
import { resumableUpload, resumableDownload, relativeUploadPath, type DownloadSession } from "../transfer";
import { TextEditor } from "./text-editor";
import { useTranslation } from "../../../i18n";
import { ApiClientError } from "../../../lib/api-client";
import { translateApiError } from "../../../lib/api-error";
import { useQuery } from "../../../lib/query";
import { toast } from "../../../lib/toast";
import { Button } from "../../../components/ui/button";
import { ConfirmDialog } from "../../../components/ui/confirm-dialog";
import { Dialog } from "../../../components/ui/dialog";
import * as files from "../api";
import "./files.css";

export function FilePanel({ serverId, onClose }: { serverId: number; onClose: () => void }) {
  const { t } = useTranslation();
  const { user, authEpoch } = useAuth();
  const pendingKey = `talus-sftp-upload:${user?.id ?? 0}:${serverId}`;
  const [uploadSession, setUploadSession] = useState<files.UploadSession | null>(() => files.readPendingUpload(pendingKey));
  const [downloadSession, setDownloadSession] = useState<DownloadSession | null>(null);
  const [editor, setEditor] = useState<files.RemoteText | null>(null);
  const resumeInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const updateUploadSession = (value: files.UploadSession | null) => { setUploadSession(value); files.storePendingUpload(pendingKey, value); };
  const [path, setPath] = useState(".");
  const [inputPath, setInputPath] = useState(".");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [progress, setProgress] = useState({ loaded: 0, total: 0 });
  const [dialog, setDialog] = useState<{ kind: "mkdir" | "rename"; from?: string; value: string } | null>(null);
  const [deletion, setDeletion] = useState<files.FileEntry | null>(null);
  const [replacement, setReplacement] = useState<{ path: string; file: File } | null>(null);
  const controller = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const uploadInput = useRef<HTMLInputElement>(null);
  const directory = useQuery({ queryKey: ["files", serverId, path], queryFn: ({ signal }) => files.listFiles(serverId, path, signal), retry: 0 });
  const currentPath = directory.data?.path ?? path;
  useEffect(() => {
    mounted.current = true;
    setUploadSession(files.readPendingUpload(pendingKey)); setDownloadSession(null); setEditor(null);
    return () => { mounted.current = false; controller.current?.abort(); controller.current = null; };
  }, [pendingKey, authEpoch]);
  useEffect(() => { if (directory.data) setInputPath(directory.data.path); }, [directory.data]);
  const navigate = (next: string) => { if (!controller.current) { setPath(next); setInputPath(next); setError(null); } };

  const run = async (label: string, operation: (signal: AbortSignal) => Promise<unknown>, refresh = false) => {
    if (controller.current) return;
    const active = new AbortController(); controller.current = active;
    setBusy(label); setError(null); setProgress({ loaded: 0, total: 0 });
    try {
      const result = await operation(active.signal);
      if (result === false) return;
      if (!mounted.current || controller.current !== active || active.signal.aborted) return;
      if (refresh) await directory.refetch();
      toast.success(t(refresh ? "files.changed" : "files.complete"));
    } catch (error) {
      if (!mounted.current || controller.current !== active) return;
      if (active.signal.aborted) toast.success(t("files.cancelled"));
      else setError(translateApiError(error, t, t("files.error")));
    } finally {
      if (controller.current === active) { controller.current = null; if (mounted.current) { setBusy(null); setDeletion(null); setDialog(null); } }
    }
  };
  const onProgress = (loaded: number, total: number) => { if (mounted.current) setProgress({ loaded, total }); };
  const uploadFile = (file: File, destination: string, overwrite: boolean, session?: files.UploadSession | null) => {
    if (file.size > files.MAX_FILE_BYTES) { setError(t("errors.sftp_too_large")); return; }
    void run(file.name, async (signal) => {
      try { await resumableUpload(serverId, destination, file, overwrite, { signal, onProgress, session, onSession: value => { if (!signal.aborted) updateUploadSession(value); } }); }
      catch (error) {
        if (session && error instanceof ApiClientError && error.reason === "sftp_checksum_mismatch") { setError(t("files.resumeMismatch")); return false; }
        if (error instanceof ApiClientError && error.reason === "sftp_exists" && !overwrite) {
          setReplacement({ file, path: destination }); return false;
        }
        throw error;
      }
    }, true);
  };
  const downloadFile = (entry: files.FileEntry, session?: DownloadSession) => {
    if (entry.size > files.MAX_FILE_BYTES) { setError(t("errors.sftp_too_large")); return; }
    const active = session ?? (downloadSession?.path === entry.path ? downloadSession : null) ?? { path: entry.path, name: entry.name, size: entry.size, offset: 0, etag: null, parts: [] };
    setDownloadSession(active);
    void run(entry.name, async signal => {
      const blob = await resumableDownload(serverId, active, signal, onProgress);
      if (!signal.aborted) { files.saveDownload(blob, entry.name); setDownloadSession(null); }
    });
  };
  const uploadFolder = (selection: File[]) => {
    if (!selection.length) return;
    let total = 0;
    const planned: { file: File; destination: string }[] = [];
    try {
      if (selection.length > 2000) throw new ApiClientError(413, { error: { code: 413, reason: "sftp_directory_too_large", message: "" } });
      for (const file of selection) {
        total += file.size;
        if (total > files.MAX_FILE_BYTES) throw new ApiClientError(413, { error: { code: 413, reason: "sftp_too_large", message: "" } });
        planned.push({ file, destination: files.joinRemote(currentPath, relativeUploadPath(file.webkitRelativePath)) });
      }
    } catch (error) { setError(translateApiError(error, t, t("files.error"))); return; }
    void run(t("files.uploadFolder"), async signal => {
      const created = new Set<string>(); let loaded = 0;
      try {
        for (const { file, destination } of planned) {
          const parent = destination.slice(0, destination.lastIndexOf("/"));
          if (!created.has(parent)) { await files.mkdirParents(serverId, parent, signal); created.add(parent); }
          await files.upload(serverId, destination, file, false, { signal, onProgress: count => onProgress(loaded + count, total) });
          loaded += file.size; onProgress(loaded, total);
        }
      } catch (error) { if (!signal.aborted) setError(`${translateApiError(error, t, t("files.error"))} ${t("files.folderPartial")}`); return false; }
    }, true);
  };
  const discardPartialUpload = () => {
    if (!uploadSession) return;
    void run(t("files.discardUpload"), async signal => {
      try { await files.cancelUpload(serverId, uploadSession.token!, signal); }
      catch (error) {
        if (!signal.aborted && error instanceof ApiClientError && ["sftp_conflict", "sftp_invalid_upload", "sftp_not_found"].includes(error.reason ?? "")) {
          updateUploadSession(null); setError(t("files.forgottenUpload")); return false;
        }
        throw error;
      }
      if (!signal.aborted) updateUploadSession(null);
    }, true);
  };
  const submit = (event: FormEvent) => {
    event.preventDefault(); if (!dialog?.value) return;
    const value = dialog.value;
    void run(t(`files.${dialog.kind}`), signal => dialog.kind === "mkdir" ? files.mkdir(serverId, value, signal) : files.rename(serverId, dialog.from!, value, signal), true);
  };

  return <aside className="sftp-panel" aria-label={t("files.title")} data-testid="file-panel">
    <header className="sftp-header"><div><h2>{t("files.title")}</h2><p>{t("files.hint")}</p></div>
      <button type="button" className="ssh-control touch-target" aria-label={t("files.close")} onClick={onClose}><X size={16} /></button>
    </header>
    <form className="sftp-path" onSubmit={event => { event.preventDefault(); navigate(inputPath); }}>
      <button type="button" className="ssh-control touch-target" aria-label={t("files.parent")} disabled={!!busy || !directory.data} onClick={() => navigate(directory.data!.parent)}><ArrowUp size={16} /></button>
      <input aria-label={t("files.path")} value={inputPath} onChange={event => setInputPath(event.target.value)} disabled={!!busy} spellCheck={false} />
      <button type="submit" className="ssh-control touch-target" disabled={!!busy}>{t("files.go")}</button>
    </form>
    <div className="sftp-actions">
      <input ref={element => { folderInput.current = element; element?.setAttribute("webkitdirectory", ""); }} type="file" multiple hidden aria-label={t("files.uploadFolder")} onChange={event => { uploadFolder(Array.from(event.target.files ?? [])); event.target.value = ""; }} />
      <input ref={resumeInput} type="file" hidden aria-label={t("files.resumeUpload")} onChange={event => { const file = event.target.files?.[0]; if (file && uploadSession) uploadFile(file, uploadSession.path, false, uploadSession); event.target.value = ""; }} />
      <button type="button" className="ssh-control touch-target" disabled={!!busy || !!uploadSession} onClick={() => uploadInput.current?.click()}><Upload size={14} />{t("files.upload")}</button>
      <input ref={uploadInput} type="file" hidden aria-label={t("files.upload")} onChange={event => { const file = event.target.files?.[0]; if (file) uploadFile(file, files.joinRemote(currentPath, file.name), false); event.target.value = ""; }} />
      <button type="button" className="ssh-control touch-target" disabled={!!busy || !!uploadSession} onClick={() => folderInput.current?.click()}><FolderPlus size={14} />{t("files.uploadFolder")}</button>
      <button type="button" className="ssh-control touch-target" disabled={!!busy} onClick={() => setDialog({ kind: "mkdir", value: files.joinRemote(currentPath, "") })}><FolderPlus size={14} />{t("files.mkdir")}</button>
      <button type="button" className="ssh-control touch-target" aria-label={t("files.refresh")} disabled={!!busy || directory.isFetching} onClick={() => void directory.refetch()}><RefreshCw size={14} className={directory.isFetching ? "animate-spin" : ""} /></button>
    </div>

    {uploadSession && !busy && <div className="sftp-paused" role="status"><p>{t("files.pausedUpload", { name: uploadSession.path.split("/").at(-1) ?? "" })}</p><div>
      <button type="button" className="ssh-control touch-target" onClick={() => resumeInput.current?.click()}>{t("files.resumeUpload")}</button>
      <button type="button" className="ssh-control touch-target" onClick={discardPartialUpload}>{t("files.discardUpload")}</button>
    </div></div>}
    {downloadSession && !busy && <div className="sftp-paused" role="status"><p>{t("files.pausedDownload", { name: downloadSession.name })}</p><div>
      <button type="button" className="ssh-control touch-target" onClick={() => downloadFile({ ...downloadSession, kind: "file", mode: "", modified_at: "" }, downloadSession)}>{t("files.resumeDownload")}</button>
      <button type="button" className="ssh-control touch-target" onClick={() => setDownloadSession(null)}>{t("files.discardDownload")}</button>
    </div></div>}
    {(error || directory.isError) && <p role="alert" className="sftp-error">{error ?? translateApiError(directory.error, t, t("files.error"))}</p>}
    {busy && <div className="sftp-transfer" role="status" aria-live="polite">
      <div><span>{progress.total > 0 && progress.loaded >= progress.total ? t("files.processing") : t("files.transfer", { name: busy })}</span><button type="button" className="ssh-control touch-target" aria-label={t("files.cancel")} onClick={() => controller.current?.abort()}><X size={14} /></button></div>
      <progress max={progress.total || 1} value={progress.total ? progress.loaded : undefined} />
      {progress.total > 0 && <small>{files.fileSize(progress.loaded)} / {files.fileSize(progress.total)}</small>}
    </div>}
    <div className="sftp-list" aria-busy={directory.isFetching || !!busy}>
      {directory.isLoading && <p className="sftp-empty"><Loader2 size={16} className="animate-spin" />{t("files.loading")}</p>}
      {directory.data?.entries.length === 0 && <p className="sftp-empty">{t("files.empty")}</p>}
      {directory.data?.entries.map(entry => {
        const Icon = entry.kind === "directory" ? Folder : entry.kind === "symlink" ? Link2 : entry.kind === "file" ? File : FileQuestion;
        return <div className="sftp-entry" key={entry.name} data-testid="file-entry">
          <button className="sftp-entry-name touch-target" type="button" disabled={!!busy || entry.kind !== "directory"} onClick={() => navigate(entry.path)} title={entry.name}><Icon size={16} /><span>{entry.name}</span></button>
          <small title={`${entry.mode} · ${entry.modified_at}`}>{entry.kind === "directory" ? "—" : files.fileSize(entry.size)}</small>
          <div className="sftp-entry-actions">
            {entry.kind === "file" && entry.size <= 1024 * 1024 && <button type="button" className="ssh-control touch-target" aria-label={`${t("files.edit")} ${entry.name}`} disabled={!!busy} onClick={() => void run(entry.name, async signal => { const text = await files.readText(serverId, entry.path, signal); if (!signal.aborted) setEditor(text); return false; })}><FilePenLine size={14} /></button>}
            {entry.kind === "directory" && <button type="button" className="ssh-control touch-target" aria-label={`${t("files.downloadFolder")} ${entry.name}`} title={t("files.archiveHint")} disabled={!!busy} onClick={() => void run(entry.name, async signal => { const blob = await files.archive(serverId, entry.path, { signal, onProgress }); if (!signal.aborted) files.saveDownload(blob, `${entry.name}.zip`); })}><Download size={14} /></button>}
            {entry.kind === "file" && <button type="button" className="ssh-control touch-target" aria-label={`${t("files.download")} ${entry.name}`} disabled={!!busy} onClick={() => downloadFile(entry)}><Download size={14} /></button>}
            <button type="button" className="ssh-control touch-target" aria-label={`${t("files.rename")} ${entry.name}`} disabled={!!busy} onClick={() => setDialog({ kind: "rename", from: entry.path, value: entry.path })}><Pencil size={14} /></button>
            <button type="button" className="ssh-control touch-target" aria-label={`${t("files.delete")} ${entry.name}`} disabled={!!busy} onClick={() => setDeletion(entry)}><Trash2 size={14} /></button>
          </div>
        </div>;
      })}
      {directory.data?.truncated && <p className="sftp-empty">{t("files.limit")}</p>}
    </div>
    <ConfirmDialog open={!!deletion} title={t("files.deleteTitle")} message={t("files.deleteBody", { path: deletion?.path ?? "" })} isPending={!!busy} onCancel={() => setDeletion(null)} onConfirm={() => { if (deletion) void run(deletion.name, signal => files.remove(serverId, deletion.path, signal), true); }} />
    <ConfirmDialog open={!!replacement} title={t("files.overwriteTitle")} message={t("files.overwriteBody", { path: replacement?.path ?? "" })} confirmLabel={t("files.save")} onCancel={() => setReplacement(null)} onConfirm={() => { if (replacement) { const { file, path } = replacement; setReplacement(null); uploadFile(file, path, true); } }} />
    {editor && <TextEditor serverId={serverId} initial={editor} onClose={() => setEditor(null)} onSaved={() => void directory.refetch()} />}
    {dialog && <Dialog open title={t(`files.${dialog.kind}`)} onClose={() => { if (!busy) setDialog(null); }} dismissible={!busy} className="max-w-lg">
      <form onSubmit={submit} className="space-y-4"><label className="block text-sm">{t(dialog.kind === "mkdir" ? "files.folderPath" : "files.newPath")}<input autoFocus className="mt-2 w-full rounded-lg border border-border bg-background p-2 font-mono text-sm" value={dialog.value} onChange={event => setDialog({ ...dialog, value: event.target.value })} disabled={!!busy} /></label>
        <Button type="submit" disabled={!!busy || !dialog.value}>{busy && <Loader2 className="h-4 w-4 animate-spin" />}{t("files.save")}</Button>
      </form>
    </Dialog>}
  </aside>;
}
